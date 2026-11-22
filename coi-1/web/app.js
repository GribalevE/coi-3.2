/* web/app.js — интерфейс проверки поручения */

(function () {
    'use strict';

    var form = document.getElementById('form');
    var resultBox = document.getElementById('result');
    var checkBtn = document.getElementById('checkBtn');
    var exampleBtn = document.getElementById('exampleBtn');
    var ruleVersion = document.getElementById('ruleVersion');
    var shiftBtn = document.getElementById('shiftBtn');
    var shiftBody = document.getElementById('shiftBody');
    var shiftStatus = document.getElementById('shiftStatus');

    var btnText = checkBtn.querySelector('.btn-text');
    var btnProgress = checkBtn.querySelector('.btn-progress');

    /* Состав смены: четыре случайных должностных лица из реестра.
       Выбор приходит с сервера (/api/shift), повторов не бывает. */
    var shift = { members: [], selectedId: '' };

    /* Сколько знаков показывать до раскрытия. Полный текст не теряется:
       остаток лежит в свёрнутом виде и открывается кнопкой. */
    var JUSTIFY_LIMIT = 240;

    /* Идентификаторы лиц в реестре (DL-014, P-185) в тексте не показываются:
       это внутренние ключи, для чтения они лишние. */
    var PERSON_ID = '(?:DL|P)-\\d{1,5}';
    var ID_LIST_IN_PARENS = new RegExp('\\(\\s*' + PERSON_ID + '(?:\\s*[,;/]\\s*' + PERSON_ID + ')*\\s*\\)', 'g');
    var ID_ANY = new RegExp('\\b' + PERSON_ID + '\\b', 'g');
    var ID_ONLY = new RegExp('^' + PERSON_ID + '(?:\\s*[,;/]\\s*' + PERSON_ID + ')*$');

    var EXAMPLE = {
        dltoPassport: '6605 856401',
        dltoFio: 'ЗАЙЦЕВ АЛЕКСЕЙ ПАВЛОВИЧ',
        personPassport: '6697 478160',
        personFio: 'ЗАЙЦЕВ ЕГОР СЕРГЕЕВИЧ',
        personAddress: 'г. Нижний Тагил, ул. ГАГАРИНА, д. 100, кв. 61'
    };

    var FACT_LABELS = {
        civil_record_id: 'Запись акта',
        address: 'Адрес',
        common_child_id: 'Общий ребёнок',
        intermediate_person_id: 'Общий родственник',
        pair: 'Пара',
        shared_ogrnip: 'ОГРНИП',
        capacity: 'Дееспособность',
        person_id: 'Лицо',
        citizenship_count: 'Количество гражданств',
        street: 'Улица',
        district_threshold_met: 'Порог района',
        identity_method: 'Способ идентификации',
        classification: 'Классификация',
        path_length: 'Длина связи',
        reason: 'Причина'
    };

    var FACT_VALUES = {
        registry_anomaly_not_joint_enterprise: 'аномалия реестра, не совместное предпринимательство',
        identity_error_warning_not_coi: 'ошибка идентификации, не конфликт интересов',
        informational_only: 'справочно',
        two_parent_edges: 'вывод по двум родительским связям',
        passport: 'по паспорту',
        name_address: 'по ФИО и адресу',
        alternative_id: 'по СНИЛС или ИНН',
        limited: 'ограниченно дееспособное лицо',
        incapable: 'недееспособное лицо',
        full: 'дееспособное лицо',
        person_not_in_ern: 'лицо отсутствует в ЕРН'
    };

    /* ---------- маска паспорта ---------- */

    ['dltoPassport', 'personPassport'].forEach(function (id) {
        var input = document.getElementById(id);
        if (!input) return;
        input.addEventListener('input', function () {
            input.value = formatPassport(input.value);
        });
    });

    function formatPassport(value) {
        var digits = String(value === undefined || value === null ? '' : value)
            .replace(/\D/g, '').slice(0, 10);
        return digits.length > 4 ? digits.slice(0, 4) + ' ' + digits.slice(4) : digits;
    }

    /* ---------- пример ---------- */

    exampleBtn.addEventListener('click', function () {
        document.getElementById('dltoPassport').value = EXAMPLE.dltoPassport;
        document.getElementById('dltoFio').value = EXAMPLE.dltoFio;
        document.getElementById('personPassport').value = EXAMPLE.personPassport;
        document.getElementById('personFio').value = EXAMPLE.personFio;
        document.getElementById('personAddress').value = EXAMPLE.personAddress;
        shift.selectedId = '';
        var shiftSelect = document.getElementById('dltoSelect');
        if (shiftSelect) shiftSelect.value = '';
        resultBox.innerHTML = '';
        renderEmpty();
        document.getElementById('dltoPassport').focus();
    });

    /* ---------- смена ---------- */

    shiftBtn.addEventListener('click', function () {
        var label = shiftBtn.textContent;
        shiftBtn.disabled = true;
        shiftBtn.textContent = 'Формирование…';

        fetch('/api/shift', { headers: { 'Accept': 'application/json' } })
            .then(function (response) { return response.json(); })
            .then(function (data) {
                if (!data || data.ok !== true) {
                    throw new Error((data && data.error) || 'Смена не сформирована.');
                }
                renderShift(data);
            })
            .catch(function (err) {
                shiftStatus.textContent = (err && err.message) || 'Смена не сформирована.';
            })
            .then(function () {
                shiftBtn.disabled = false;
                shiftBtn.textContent = label;
            });
    });

    /* Выбор должностного лица из состава смены заполняет форму проверки. */
    shiftBody.addEventListener('change', function (event) {
        var select = event.target;
        if (!select || select.id !== 'dltoSelect') return;

        shift.selectedId = select.value;
        if (!select.value) return;

        for (var i = 0; i < shift.members.length; i++) {
            if (shift.members[i].id !== select.value) continue;
            var chosen = shift.members[i];
            document.getElementById('dltoFio').value = chosen.full_name || '';
            document.getElementById('dltoPassport').value = formatPassport(chosen.passport);
            break;
        }
    });

    function renderShift(data) {
        shift.members = (data && data.members) || [];
        shift.selectedId = '';

        var html = '<ul class="shift-team">';
        for (var i = 0; i < shift.members.length; i++) {
            var member = shift.members[i];
            html += '<li class="shift-person">';
            html += '<span class="shift-name">' + esc(member.full_name || '') + '</span>';
            if (member.post) {
                html += '<span class="shift-post">' + esc(member.post) + '</span>';
            }
            if (member.passport) {
                html += '<span class="shift-passport">паспорт ' +
                    esc(formatPassport(member.passport)) + '</span>';
            }
            html += '</li>';
        }
        html += '</ul>';

        html += '<div class="field shift-field">';
        html += '<label for="dltoSelect">Должностное лицо, выполняющее досмотр</label>';
        html += '<select id="dltoSelect" name="dlto_select">';
        html += '<option value="">— выберите из состава смены —</option>';
        for (var j = 0; j < shift.members.length; j++) {
            var item = shift.members[j];
            html += '<option value="' + esc(item.id) + '">' + esc(item.full_name || '');
            if (item.post) html += ' — ' + esc(item.post);
            html += '</option>';
        }
        html += '</select>';
        html += '<p class="field-hint">Выбор заполняет сведения о должностном лице ' +
            'в форме проверки.</p>';
        html += '</div>';

        shiftBody.innerHTML = html;
        shiftStatus.textContent = 'Смена начата в ' + (data.started_at || '') +
            ', на смене ' + shift.members.length + ' должностных лица.';
    }

    /* ---------- раскрытие сокращённого текста ---------- */

    resultBox.addEventListener('click', function (event) {
        var btn = event.target.closest ? event.target.closest('.link-more') : null;
        if (!btn) return;
        var scope = btn.parentNode;
        var rest = scope.querySelector('.rest');
        var dots = scope.querySelector('.dots');
        if (rest) rest.hidden = false;
        if (dots) dots.hidden = true;
        btn.parentNode.removeChild(btn);
    });

    /* ---------- отправка ---------- */

    form.addEventListener('submit', function (event) {
        event.preventDefault();

        var payload = {
            dlto: {
                passport: document.getElementById('dltoPassport').value.trim(),
                full_name: document.getElementById('dltoFio').value.trim()
            },
            person: {
                passport: document.getElementById('personPassport').value.trim(),
                full_name: document.getElementById('personFio').value.trim(),
                address: document.getElementById('personAddress').value.trim()
            }
        };

        /* Должностное лицо выбрано из состава смены: сервер восстановит
           его сведения по идентификатору, паспорт в браузер не передаётся. */
        if (shift.selectedId) {
            payload.dlto_source = shift.selectedId;
        }

        if (!payload.dlto.passport && !payload.dlto.full_name) {
            renderError('Укажите сведения о должностном лице: серию и номер паспорта, ' +
                'ФИО или выберите его из состава смены.');
            return;
        }
        if (!payload.person.passport && !payload.person.full_name) {
            renderError('Укажите сведения о проверяемом лице: серию и номер паспорта либо ФИО.');
            return;
        }

        setLoading(true);
        resultBox.setAttribute('aria-busy', 'true');

        fetch('/api/check', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json', 'Accept': 'application/json' },
            body: JSON.stringify(payload)
        })
            .then(function (response) {
                return response.json().then(function (data) {
                    return { status: response.status, data: data };
                });
            })
            .then(function (res) {
                if (!res.data || res.data.ok !== true) {
                    renderError((res.data && res.data.error) || 'Проверка не выполнена.');
                    return;
                }
                renderResult(res.data);
            })
            .catch(function () {
                renderError('Сервис проверки недоступен. Повторите попытку позже.');
            })
            .then(function () {
                setLoading(false);
                resultBox.removeAttribute('aria-busy');
            });
    });

    function setLoading(on) {
        checkBtn.disabled = on;
        btnText.hidden = on;
        btnProgress.hidden = !on;
    }

    /* ---------- отрисовка ---------- */

    function renderEmpty() {
        resultBox.innerHTML =
            '<div class="state state-empty">' +
            '<p class="state-title">Проверка не выполнялась</p>' +
            '<p class="state-text">Заполните сведения о должностном лице и проверяемом лице ' +
            'либо воспользуйтесь демонстрационным примером.</p>' +
            '</div>';
    }

    function renderError(message) {
        resultBox.innerHTML =
            '<div class="state state-error">' +
            '<p class="state-title">Проверка не выполнена</p>' +
            '<p class="state-text">' + esc(message) + '</p>' +
            '</div>';
    }

    function renderResult(data) {
        var signal = data.signal || {};
        var reasons = signal.reasons || [];

        var html = '';

        html += '<div class="result-meta">';
        html += '<span>Поручение: <b>' + esc(data.order_id || '—') + '</b></span>';
        html += '<span>Проверка выполнена: <b>' + esc(data.checked_at || '—') + '</b></span>';
        html += '<span>Набор правил: <b>' + esc(data.rule_version || '—') + '</b></span>';
        html += '</div>';

        html += '<table class="pair"><tbody>';
        html += pairRow('Должностное лицо', data.dlto);
        html += pairRow('Проверяемое лицо', data.person);
        html += '</tbody></table>';

        html += '<div class="block">';
        html += '<p class="block-title">Обоснование</p>';
        html += folded(stripIds(data.justification), 'justify', JUSTIFY_LIMIT);
        html += '</div>';

        html += '<div class="block">';
        html += '<p class="block-title">Заключение</p>';
        html += '<div class="conclusion ' + conclusionTone(signal) + '">' +
            esc(conclusionText(signal)) + '</div>';
        html += '</div>';

        if (reasons.length) {
            html += '<div class="block">';
            html += '<p class="block-title">Установленные обстоятельства</p>';
            html += '<ul class="findings">';
            for (var i = 0; i < reasons.length; i++) {
                html += findingItem(reasons[i]);
            }
            html += '</ul>';
            html += '</div>';
        }

        resultBox.innerHTML = html;

        if (data.rule_version && ruleVersion) {
            ruleVersion.textContent = data.rule_version;
        }
    }

    function pairRow(label, person) {
        if (!person) return '';
        var value = esc(person.full_name || '—');
        if (person.post) {
            value += '<br><span class="detail">' + esc(person.post) + '</span>';
        }
        if (person.address) {
            value += '<br><span class="detail">' + esc(person.address) + '</span>';
        }
        return '<tr><th>' + esc(label) + '</th><td>' + value + '</td></tr>';
    }

    /* Сокращённый текст: первая часть видна сразу, остаток — по кнопке. */
    function folded(text, cls, limit) {
        var parts = splitText(text, limit);
        if (!parts.tail) {
            return '<p class="' + cls + '">' + esc(parts.head) + '</p>';
        }
        return '<p class="' + cls + '">' + esc(parts.head) +
            '<span class="dots">…</span>' +
            '<span class="rest" hidden>' + esc(parts.tail) + '</span></p>' +
            '<button type="button" class="link-more">Показать полностью</button>';
    }

    function splitText(text, limit) {
        var full = String(text === undefined || text === null ? '' : text).trim();
        if (full.length <= limit) {
            return { head: full, tail: '' };
        }
        var cut = full.slice(0, limit);
        var stop = Math.max(cut.lastIndexOf('. '), cut.lastIndexOf('; '), cut.lastIndexOf(', '));
        if (stop < limit * 0.5) stop = cut.lastIndexOf(' ');
        if (stop < limit * 0.5) stop = cut.length;
        var head = full.slice(0, stop).replace(/[\s.,;:]+$/, '');
        return { head: head, tail: full.slice(stop).replace(/^[\s.,;:]+/, '') };
    }

    /* Идентификаторы лиц из текста обоснования убираются вместе со скобками,
       если в скобках были только они. */
    function stripIds(text) {
        var s = String(text === undefined || text === null ? '' : text);
        s = s.replace(ID_LIST_IN_PARENS, '');
        s = s.replace(ID_ANY, '');
        s = s.replace(/\s+([,.;:])/g, '$1');
        s = s.replace(/,\s*,/g, ',');
        s = s.replace(/\(\s*\)/g, '');
        s = s.replace(/\s{2,}/g, ' ');
        return s.trim();
    }

    /* Короткое заключение: три формулировки по вероятности взаимосвязи. */
    function conclusionText(signal) {
        var severity = signal && signal.severity ? signal.severity : '';
        switch (severity) {
            case 'critical':
            case 'high':
                return 'Установлена высокая вероятность взаимосвязи лиц';
            case 'medium':
                return 'Установлена средняя вероятность взаимосвязи лиц';
            case 'low':
            case 'warning':
            case 'info':
                return 'Установлена низкая вероятность взаимосвязи лиц';
            case 'none':
                return 'Взаимосвязь лиц не установлена';
            default:
                return 'Сведений недостаточно для вывода о взаимосвязи лиц';
        }
    }

    function findingItem(reason) {
        var html = '<li class="finding">';
        html += '<div class="finding-head">';
        html += '<span class="finding-code">' + esc(reason.code || '') + '</span>';
        html += '<span class="finding-title">' + esc(reason.title || '') + '</span>';
        html += '</div>';

        var facts = reason.facts || [];
        var rows = '';
        for (var i = 0; i < facts.length; i++) {
            var fact = facts[i];
            if (!fact || !fact.value) continue;
            /* Значение, состоящее только из идентификаторов лиц, не выводим. */
            if (ID_ONLY.test(String(fact.value).trim())) continue;
            var key = FACT_LABELS[fact.key] || fact.key;
            var value = FACT_VALUES[fact.value] || fact.value;
            rows += '<li>' + esc(key) + ': <span>' + esc(value) + '</span></li>';
        }
        if (rows) {
            html += '<ul class="finding-facts">' + rows + '</ul>';
        }
        html += '</li>';
        return html;
    }

    function conclusionTone(signal) {
        var severity = signal && signal.severity ? signal.severity : '';
        if (severity === 'critical' || severity === 'high' || severity === 'medium') return 'is-attention';
        if (severity === 'low' || severity === 'warning' || severity === 'info') return 'is-partial';
        if (severity === 'none') return 'is-clear';
        return 'is-partial';
    }

    function esc(value) {
        return String(value === undefined || value === null ? '' : value).replace(/[&<>"']/g, function (c) {
            return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
        });
    }

    renderEmpty();
})();
