// cmd/seed/main.go

// Command seed конвертирует SQL-дамп ЕРН в два отдельных файла датасета:
//
//	data/dataset.json  — лица (без деления на физических и должностных лиц)
//	data/dltos.json    — реестр должностных лиц (правится через /admin)
//
//	go run ./cmd/seed -in data/ern_full.sql
//
// Конвертация делается один раз (или при обновлении дампа). Приложение читает
// только эти файлы: мок СМЭВ и реестр идентификации работают с одним и тем же
// набором данных, SQL-дамп в рантайме не нужен.
package main

import (
	"context"
	"database/sql"
	"flag"
	"log"
	"strings"
	"time"

	"coi-1/internal/data"
	"coi-1/internal/erndb"
)

func main() {
	in := flag.String("in", "data/ern_full.sql", "path to ERN SQL dump")
	persons := flag.String("persons", "data/dataset.json", "output: persons dataset")
	dltos := flag.String("dltos", "data/dltos.json", "output: officials dataset")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	db, err := erndb.Open(*in)
	if err != nil {
		log.Fatalf("open dump: %v", err)
	}
	defer db.Close()

	bundle, err := erndb.Bundle(ctx, db)
	if err != nil {
		log.Fatalf("read dump: %v", err)
	}

	posts := loadPosts(ctx, db)

	filled, skipped := fillCivilEvidence(&bundle)
	normalizeCitizenships(&bundle)
	removed := dedupe(&bundle)

	// Деление на физических и должностных лиц убираем из основного датасета:
	// роль остаётся только в реестре должностных лиц (отдельный файл).
	personsBundle, officialsSet := split(bundle, posts)
	officialsSet, droppedOfficials := dropIncompleteOfficials(officialsSet)
	if droppedOfficials > 0 {
		log.Printf("WARNING: из реестра должностных лиц исключено %d записей без ФИО "+
			"или паспорта — идентификация по ним невозможна", droppedOfficials)
	}

	personsBundle.Canonicalize()
	if err := personsBundle.Validate(); err != nil {
		log.Fatalf("persons dataset is not consistent: %v", err)
	}
	if err := erndb.SaveBundle(*persons, personsBundle); err != nil {
		log.Fatalf("write persons dataset: %v", err)
	}

	if err := data.SaveDLTOSet(*dltos, officialsSet); err != nil {
		log.Fatalf("write officials dataset: %v", err)
	}

	log.Printf("persons dataset: %s", *persons)
	log.Printf("  person=%d, kinship=%d, civil=%d, divorce=%d, guardianship=%d, "+
		"emancipation=%d, parental=%d, capacity=%d, employment=%d, tax=%d",
		len(personsBundle.Persons), len(personsBundle.Kinship), len(personsBundle.Civil),
		len(personsBundle.Divorces), len(personsBundle.Guardianships),
		len(personsBundle.Emancipations), len(personsBundle.Parental),
		len(personsBundle.Capacity), len(personsBundle.Employment), len(personsBundle.Tax))
	log.Printf("officials dataset: %s (%d записей)", *dltos, len(officialsSet.Persons))
	log.Printf("civil evidence restored: %d, skipped (родительская связь в обратном "+
		"направлении): %d, duplicate rows removed: %d", filled, skipped, removed)

	checkCitizenship(personsBundle)
	checkDemoPair(personsBundle, officialsSet)
}

// split делит лица на основной датасет и реестр должностных лиц.
// В основном датасете роль очищается у всех без исключения.
func split(b data.Bundle, posts map[string]string) (data.Bundle, data.DLTOSet) {
	persons := make([]data.Person, 0, len(b.Persons))
	officials := data.DLTOSet{Persons: make([]data.Person, 0, 32)}

	for _, p := range b.Persons {
		role := p.Role
		p.Role = ""
		persons = append(persons, p)

		if role != "DLTO" {
			continue
		}
		official := p
		official.Role = ""
		if post, ok := posts[p.ID]; ok && post != "" {
			official.Post = post
		}
		officials.Persons = append(officials.Persons, official)
	}

	b.Persons = persons
	return b, officials
}

// dropIncompleteOfficials убирает записи, которые не пройдут валидацию реестра:
// без паспорта должностное лицо не идентифицируется, а падение всей конвертации
// из-за одной строки недопустимо.
func dropIncompleteOfficials(set data.DLTOSet) (data.DLTOSet, int) {
	kept := make([]data.Person, 0, len(set.Persons))
	dropped := 0
	for _, p := range set.Persons {
		if strings.TrimSpace(p.FullName) == "" || data.Digits(p.Passport) == "" {
			dropped++
			continue
		}
		kept = append(kept, p)
	}
	set.Persons = kept
	return set, dropped
}

// loadPosts достаёт таможенный пост из дампа, если такая колонка есть.
// Схема «migrations» её не содержит, схема ЕРН — содержит.
func loadPosts(ctx context.Context, db *sql.DB) map[string]string {
	var has int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('person') WHERE name='customs_post'`).
		Scan(&has); err != nil || has == 0 {
		return nil
	}
	rows, err := db.QueryContext(ctx,
		`SELECT person_id, COALESCE(customs_post,'') FROM person`)
	if err != nil {
		log.Printf("WARNING: не удалось прочитать таможенные посты: %v", err)
		return nil
	}
	defer rows.Close()

	out := make(map[string]string, 64)
	for rows.Next() {
		var id, post string
		if err := rows.Scan(&id, &post); err != nil {
			continue
		}
		if post != "" {
			out[id] = strings.TrimSpace(post)
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("WARNING: чтение таможенных постов прервано: %v", err)
	}
	return out
}

// fillCivilEvidence восстанавливает ссылку kinship → civil_registry.
//
// В дампе у большинства связей нет act_number, совпадающего с записью ЗАГС,
// поэтому civil_id пуст, а K-01 требует подтверждения именно записью акта.
// Здесь ссылка достраивается по паре лиц. Для «брат/сестра» и «супруг(а)»
// направление не важно, для родительских связей берётся только точное совпадение.
func fillCivilEvidence(b *data.Bundle) (filled, skipped int) {
	direct := make(map[string]string, len(b.Civil))
	byPair := make(map[string]string, len(b.Civil))
	direction := make(map[string][2]string, len(b.Civil))

	for _, c := range b.Civil {
		if c.ID == "" {
			continue
		}
		direct[c.A+"\x00"+c.B] = c.ID
		if _, ok := byPair[pairKey(c.A, c.B)]; !ok {
			byPair[pairKey(c.A, c.B)] = c.ID
		}
		direction[c.ID] = [2]string{c.A, c.B}
	}

	for i := range b.Kinship {
		k := &b.Kinship[i]
		if k.CivilID != "" {
			continue
		}
		if id, ok := direct[k.A+"\x00"+k.B]; ok {
			k.CivilID = id
			filled++
			continue
		}
		id, ok := byPair[pairKey(k.A, k.B)]
		if !ok {
			continue
		}
		if k.Kind == "parent" {
			skipped++
			continue
		}
		d := direction[id]
		k.A, k.B = d[0], d[1]
		k.CivilID = id
		filled++
	}
	return filled, skipped
}

// normalizeCitizenships приводит значения к кодам, которые понимает
// identity.Matcher.
func normalizeCitizenships(b *data.Bundle) {
	for i := range b.Persons {
		var out []string
		seen := make(map[string]bool, 2)
		for _, c := range b.Persons[i].Citizenships {
			code := citizenshipCode(c)
			if code == "" || seen[code] {
				continue
			}
			seen[code] = true
			out = append(out, code)
		}
		b.Persons[i].Citizenships = out
	}
}

func citizenshipCode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "rf", "рф", "россия", "российская федерация", "russia", "russian federation":
		return "RF"
	case "tj", "таджикистан", "tajikistan":
		return "TJ"
	case "kz", "казахстан", "kazakhstan":
		return "KZ"
	case "kg", "киргизия", "кыргызстан", "kyrgyzstan":
		return "KG"
	case "uz", "узбекистан", "uzbekistan":
		return "UZ"
	case "am", "армения", "armenia":
		return "AM"
	case "az", "азербайджан", "azerbaijan":
		return "AZ"
	case "by", "беларусь", "белоруссия", "belarus":
		return "BY"
	case "ua", "украина", "ukraine":
		return "UA"
	default:
		return strings.ToUpper(strings.TrimSpace(raw))
	}
}

// checkCitizenship предупреждает о лицах без гражданства: по ним идентификация
// вернёт «лицо не является гражданином РФ».
func checkCitizenship(b data.Bundle) {
	var unknown []string
	for _, p := range b.Persons {
		if len(p.Citizenships) == 0 {
			unknown = append(unknown, p.ID)
		}
	}
	if len(unknown) == 0 {
		return
	}
	shown := unknown
	if len(shown) > 10 {
		shown = shown[:10]
	}
	log.Printf("WARNING: у %d лиц не указано гражданство — идентификация по ним невозможна "+
		"(первые: %s)", len(unknown), strings.Join(shown, ", "))
}

// dedupe убирает повторы, которые data.Bundle.Validate считает ошибкой.
func dedupe(b *data.Bundle) int {
	removed := 0

	seenKin := map[string]bool{}
	kin := b.Kinship[:0]
	for _, k := range b.Kinship {
		key := k.A + "\x00" + k.B + "\x00" + k.Kind
		if seenKin[key] {
			removed++
			continue
		}
		seenKin[key] = true
		kin = append(kin, k)
	}
	b.Kinship = kin

	seenPair := map[string]bool{}
	div := b.Divorces[:0]
	for _, d := range b.Divorces {
		key := pairKey(d.A, d.B)
		if seenPair[key] {
			removed++
			continue
		}
		seenPair[key] = true
		div = append(div, d)
	}
	b.Divorces = div

	seenCap := map[string]bool{}
	capSlice := b.Capacity[:0]
	for _, c := range b.Capacity {
		if seenCap[c.PersonID] {
			removed++
			continue
		}
		seenCap[c.PersonID] = true
		capSlice = append(capSlice, c)
	}
	b.Capacity = capSlice

	seenEmp := map[string]bool{}
	emp := b.Employment[:0]
	for _, e := range b.Employment {
		if seenEmp[e.PersonID] {
			removed++
			continue
		}
		seenEmp[e.PersonID] = true
		emp = append(emp, e)
	}
	b.Employment = emp

	seenTax := map[string]bool{}
	tax := b.Tax[:0]
	for _, t := range b.Tax {
		if seenTax[t.PersonID] {
			removed++
			continue
		}
		seenTax[t.PersonID] = true
		tax = append(tax, t)
	}
	b.Tax = tax

	seenDL := map[string]bool{}
	dl := b.DriverLicenses[:0]
	for _, d := range b.DriverLicenses {
		if seenDL[d.PersonID] {
			removed++
			continue
		}
		seenDL[d.PersonID] = true
		dl = append(dl, d)
	}
	b.DriverLicenses = dl

	return removed
}

func pairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "\x00" + b
}

// checkDemoPair печатает состояние демонстрационной пары.
func checkDemoPair(b data.Bundle, officials data.DLTOSet) {
	const wantPassport = "6697478160"

	var found *data.Person
	for i := range b.Persons {
		if data.Digits(b.Persons[i].Passport) == wantPassport {
			found = &b.Persons[i]
			break
		}
	}
	if found == nil {
		log.Printf("WARNING: демо-пара не найдена — в датасете нет лица с паспортом 6697 478160")
		return
	}

	want := pairKey("DL-014", found.ID)
	var edges []string
	hasCivil := false
	for _, k := range b.Kinship {
		if pairKey(k.A, k.B) != want {
			continue
		}
		edge := k.A + "↔" + k.B + " (" + k.Kind + ")"
		if k.CivilID != "" {
			edge += ", ЗАГС " + k.CivilID
			hasCivil = true
		}
		edges = append(edges, edge)
	}

	if _, ok := officials.Get("DL-014"); !ok {
		log.Printf("WARNING: DL-014 отсутствует в реестре должностных лиц — " +
			"проверка по демо-паре из интерфейса не выполнится")
	}

	if len(edges) == 0 {
		log.Printf("WARNING: демо-пара DL-014/%s найдена, но родственной связи нет — "+
			"K-01 не сработает", found.ID)
		return
	}
	log.Printf("демо-пара DL-014/%s: %v", found.ID, edges)
	if !hasCivil {
		log.Printf("WARNING: у демо-пары нет записи ЗАГС — K-01 (родство) не сработает, " +
			"останутся K-02 (адрес) и K-10 (фамилия)")
	}
}
