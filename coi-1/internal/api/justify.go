// internal/api/justify.go

package api

import (
	"fmt"
	"sort"
	"strings"

	"coi-1/internal/conflict"
	"coi-1/internal/data"
)

// justify превращает сигнал в текст на русском: не «уровень риска», а
// перечисление установленных обстоятельств и вывод из них.
//
// Возвращает два блока: обоснование (что установлено) и заключение (что из
// этого следует и чего не хватает для полного вывода).
func justify(orderID string, sig conflict.Signal, dlto, person data.Person) (string, string) {
	if sig.Severity == conflict.SeverityNoData {
		reason := ""
		for _, r := range sig.Reasons {
			if v := factValue(r, "reason"); v != "" {
				reason = v
			}
		}
		text := fmt.Sprintf(
			"Проверка по поручению %s не выполнена: проверяемое лицо не идентифицировано "+
				"в едином реестре населения (причина: %s). Автоматическое заключение о наличии "+
				"или отсутствии конфликта интересов не формируется.", orderID, humanReason(reason))
		return text, "Требуется ручная проверка сведений о проверяемом лице."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "По поручению %s проверены сведения о должностном лице %s и проверяемом лице %s. ",
		orderID, nameOrID(dlto), nameOrID(person))

	if len(sig.Reasons) == 0 {
		b.WriteString("Совпадений по проверенным основаниям не установлено.")
	} else {
		clauses := make([]string, 0, len(sig.Reasons))
		for _, r := range sig.Reasons {
			clauses = append(clauses, clause(r))
		}
		b.WriteString("Установлено: ")
		b.WriteString(strings.Join(clauses, "; "))
		b.WriteString(".")
	}

	return b.String(), conclusionText(sig)
}

// clause — формулировка одного установленного обстоятельства.
func clause(r conflict.Reason) string {
	switch r.Code {
	case "K-01":
		if id := factValue(r, "civil_record_id"); id != "" {
			return "между должностным лицом и проверяемым лицом установлено родство, " +
				"подтверждённое записью акта гражданского состояния " + id
		}
		return "между должностным лицом и проверяемым лицом установлено родство"
	case "K-02":
		if addr := factValue(r, "address"); addr != "" {
			return "адрес регистрации должностного лица и проверяемого лица совпадает (" + addr + ")"
		}
		return "адрес регистрации должностного лица и проверяемого лица совпадает"
	case "K-03":
		return "между должностным лицом и проверяемым лицом зарегистрировано расторжение брака"
	case "K-04":
		if kids := joinedFacts(r, "common_child_id"); kids != "" {
			return "у должностного лица и проверяемого лица имеются общие дети (" + kids + ")"
		}
		return "у должностного лица и проверяемого лица имеются общие дети"
	case "K-05":
		if mid := joinedFacts(r, "intermediate_person_id"); mid != "" {
			return "родство установлено через общего родственника (" + mid + ")"
		}
		return "родство установлено через общего родственника"
	case "K-07":
		return "связанное лицо признано эмансипированным"
	case "K-08":
		return "полностью совпадают фамилия, имя и отчество; это признак ошибки " +
			"идентификации, а не конфликта интересов"
	case "K-09":
		return "совпадают фамилия, имя, отчество и дата рождения; это признак ошибки " +
			"идентификации, а не конфликта интересов"
	case "K-10":
		return "совпадает фамилия должностного лица и проверяемого лица"
	case "K-11":
		return "совпадают инициалы и город должностного лица и проверяемого лица"
	case "K-12":
		if street := factValue(r, "street"); street != "" {
			return "адреса находятся в пределах одной улицы (" + street + "), дом не совпадает"
		}
		return "адреса находятся в пределах одной улицы, дом не совпадает"
	case "K-13":
		return "совпадение установлено по справочнику «таможенный пост — район», " +
			"само по себе основанием не является"
	case "K-16":
		return "в отношении проверяемого лица имеется запись о лишении родительских прав"
	case "K-17":
		if st := factValue(r, "capacity"); st != "" {
			return "у проверяемого лица установлено ограничение дееспособности (" + humanCapacity(st) + ")"
		}
		return "у проверяемого лица установлено ограничение дееспособности"
	case "K-18":
		return "проверяемое лицо состоит на учёте как безработное"
	case "K-19":
		if ogrnip := joinedFacts(r, "shared_ogrnip"); ogrnip != "" {
			return "у должностного лица и проверяемого лица совпадает ОГРНИП (" + ogrnip +
				"); это аномалия реестра, а не совместное предпринимательство"
		}
		return "у должностного лица и проверяемого лица совпадает ОГРНИП"
	case "K-20":
		if n := factValue(r, "citizenship_count"); n != "" {
			return "у проверяемого лица несколько гражданств (всего " + n + ")"
		}
		return "у проверяемого лица несколько гражданств"
	default:
		return strings.ToLower(strings.TrimSpace(r.Title))
	}
}

// conclusionText — вывод и оговорка о полноте сведений.
func conclusionText(sig conflict.Signal) string {
	var parts []string

	if sig.Coverage != nil && !sig.Coverage.Complete {
		if gaps := gapList(sig.Coverage.RuleGaps); gaps != "" {
			parts = append(parts, "Заключение подготовлено по неполному набору сведений: "+
				"не проверялись основания, требующие источников "+gaps+".")
		} else {
			parts = append(parts, "Заключение подготовлено по неполному набору сведений.")
		}
	}

	switch sig.Severity {
	case conflict.SeverityCritical, conflict.SeverityHigh:
		parts = append(parts, "Установленные обстоятельства не позволяют должностному лицу "+
			"провести личный таможенный досмотр беспристрастно. Поручение подлежит передаче "+
			"другому должностному лицу либо отмене.")
	case conflict.SeverityMedium:
		parts = append(parts, "Установленные обстоятельства требуют дополнительной проверки; "+
			"окончательное решение принимает начальник таможенного поста.")
	case conflict.SeverityLow, conflict.SeverityWarning, conflict.SeverityInfo:
		parts = append(parts, "Установленные обстоятельства имеют справочный характер и сами "+
			"по себе не препятствуют проведению досмотра.")
	case conflict.SeverityNoData:
		parts = append(parts, "Проверка невозможна: сведений о проверяемом лице недостаточно.")
	default:
		parts = append(parts, "Обстоятельств, указывающих на конфликт интересов, по проверенным "+
			"источникам не установлено.")
	}

	return strings.Join(parts, " ")
}

func gapList(gaps []conflict.RuleGap) string {
	if len(gaps) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var names []string
	for _, g := range gaps {
		for _, s := range g.Sources {
			if seen[s] {
				continue
			}
			seen[s] = true
			names = append(names, humanSource(s))
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func humanSource(s string) string {
	switch s {
	case "person":
		return "«сведения о лице»"
	case "kinship":
		return "«родство»"
	case "civil_registry":
		return "«записи ЗАГС»"
	case "divorce":
		return "«расторжение брака»"
	case "emancipation":
		return "«эмансипация»"
	case "tax_registration":
		return "«налоговый учёт»"
	case "parental_rights":
		return "«родительские права»"
	case "legal_capacity":
		return "«дееспособность»"
	case "employment_service":
		return "«служба занятости»"
	case "post_district":
		return "«справочник постов»"
	default:
		return "«" + s + "»"
	}
}

func humanCapacity(state string) string {
	switch state {
	case "limited":
		return "ограниченно дееспособное лицо"
	case "incapable":
		return "недееспособное лицо"
	case "full":
		return "дееспособное лицо"
	default:
		return state
	}
}

func humanReason(reason string) string {
	switch reason {
	case "person_not_found":
		return "лицо не найдено в реестре"
	case "identity_ambiguous":
		return "найдено несколько совпадений"
	case "identity_conflict":
		return "данные не совпадают с записью в реестре"
	case "citizenship_not_rf":
		return "лицо не является гражданином Российской Федерации"
	case "":
		return "причина не указана"
	default:
		return reason
	}
}

func nameOrID(p data.Person) string {
	if strings.TrimSpace(p.FullName) != "" {
		return strings.TrimSpace(p.FullName) + " (" + p.ID + ")"
	}
	return p.ID
}

func factValue(r conflict.Reason, key string) string {
	for _, f := range r.Facts {
		if f.Key == key && strings.TrimSpace(f.Value) != "" {
			return strings.TrimSpace(f.Value)
		}
	}
	return ""
}

func joinedFacts(r conflict.Reason, key string) string {
	var out []string
	for _, f := range r.Facts {
		if f.Key == key && strings.TrimSpace(f.Value) != "" {
			out = append(out, strings.TrimSpace(f.Value))
		}
	}
	return strings.Join(out, ", ")
}

