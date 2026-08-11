package ui

import "fmt"

// RenderHint рисует строку подсказки — центральный элемент дизайна
// (FIXUI-04): GlyphHint, пробел и текст предложения; текст жирный, символ
// обычный, точка в конце не ставится. Подсказка присутствует ВСЕГДА, в
// нижней части каждого экрана, отделена одной пустой строкой от тела, и
// всегда формулирует следующее действие, а не описывает текущее
// состояние.
func RenderHint(t Theme, text string) string {
	return t.Text.Render(GlyphHint) + " " + t.HintText.Render(text)
}

// confirmKey — клавиша подтверждения текстового поля или согласия проводника
// (Фаза 13, план 13-03), прочитанная из раскладки, а не литералом:
// DefaultKeys — тот же единственный источник привязок, что и корневая модель
// (internal/ui/app.go, Run), поэтому второй живой экземпляр раскладки не
// расходится с настоящим. Формулировки, зовущие confirmKey, — не про закон
// ленты (движение вправо): поле ввода токена, поле сообщения коммита, шаг
// «выполните команду и повторите» проводника означают именно «нажмите
// клавишу подтверждения», а не «откройте колонку правее» — приписывать им
// второй смысл не нужно, поэтому они читают Open (та же физическая клавиша
// подтверждения, что и раньше), а не Right.
func confirmKey() string {
	return DefaultKeys().Open.Help().Key
}

// Формулировки строки подсказки экрана списка джоб (09-UI-SPEC.md, «Строка
// подсказки») — по одной именованной функции на строку таблицы контракта,
// чтобы каждая формулировка жила ровно в одном месте проекта. Остальные
// строки той же таблицы — состояния петли фикса, переноса правок, коммита
// и подмены образа экрана джобы — принадлежат планам 09-02 и 09-03: таблица
// контракта разложена по планам целиком, а не урезана здесь.

// HintJobFailed — в пайплайне есть упавшая джоба, ещё не открыта, и номер
// упавшего шага известен.
//
// Раздел «Copywriting Contract» контракта Фазы 9 (09-UI-SPEC.md) называет
// ровно эту формулировку («нажмите [клавиша подтверждения], чтобы
// воспроизвести её локально») дословно, клавишей подтверждения. Закон ленты
// (Фаза 13, idea-0.3.1 §3) делает эту клавишу синонимом движения вправо —
// подсказка обязана называть основную клавишу закона, а не клавишу
// подтверждения: старая клавиша продолжает работать, меняется только текст.
// Это осознанное отступление от контракта Фазы 9, зафиксированное здесь и в
// SUMMARY плана 13-03.
func HintJobFailed(name string, step, total int) string {
	return fmt.Sprintf(
		"джоба %s упала на шаге %d из %d — нажмите "+GlyphRight+", чтобы воспроизвести её локально",
		Plain(name), step, total,
	)
}

// HintJobFailedUnknownStep — та же ситуация, но номер упавшего шага
// неизвестен: конфиг джобы на экране списка не запрашивается. Отдельная
// формулировка, а не нули в подстановке: «упала на шаге 0 из 0» — такое же
// враньё числами, только менее заметное (WR-13 обзора v0.3.0). То же
// отступление от Copywriting Contract, что и у HintJobFailed выше.
func HintJobFailedUnknownStep(name string) string {
	return fmt.Sprintf(
		"джоба %s упала — нажмите "+GlyphRight+", чтобы воспроизвести её локально",
		Plain(name),
	)
}

// HintNoFailedJob — ни одна джоба не упала (всё зелёное/ещё выполняется).
// Клавиша читается из раскладки (LOOK-04), а не литералом.
func HintNoFailedJob() string {
	return fmt.Sprintf("ни одна джоба не упала — нажмите %s, чтобы обновить список", DefaultKeys().Refresh.Help().Key)
}

// HintPipelineRunning — пайплайн выполняется, есть running-джобы. Клавиша
// читается из раскладки (LOOK-04), а не литералом.
func HintPipelineRunning() string {
	return fmt.Sprintf("пайплайн ещё выполняется — %s обновит список", DefaultKeys().Refresh.Help().Key)
}

// HintPipelineEmpty — список джоб пуст (0 джоб в пайплайне).
func HintPipelineEmpty() string {
	return "в этом пайплайне нет джоб"
}

// HintRetryJobs — загрузка списка джоб провалилась. Клавиша читается из
// раскладки (LOOK-04), а не литералом.
func HintRetryJobs() string {
	return fmt.Sprintf("%s повторит запрос", DefaultKeys().Refresh.Help().Key)
}

// HintBlocked — минимальный честный фолбэк для ошибки/блокировки.
// Полноценные гайд-экраны для типичных поломок — Phase 11 (GUIDE-01…05).
func HintBlocked(reason string) string {
	return fmt.Sprintf("%s — подробности в логе", reason)
}

// Формулировки строки подсказки экрана джобы (09-UI-SPEC.md, «Строка
// подсказки», раздел «Джоба») — план 09-02. Каждая функция называет ровно
// одну строку таблицы контракта, чтобы формулировка жила в одном месте.

// HintImageReady — образ готов, шелл ещё не открывали. Ссылка на образ
// приходит из конфига джобы (.gitlab-ci.yml) и чистится здесь: переноса
// ответа API, который сделал бы это раньше, у неё нет (CR-06).
// Клавиша читается из раскладки (LOOK-04), а не литералом.
func HintImageReady(image string) string {
	return fmt.Sprintf("образ %s готов — нажмите %s, чтобы войти в контейнер", Plain(image), DefaultKeys().Shell.Help().Key)
}

// HintLeftShell — вышли из шелла (после возврата терминала), R ещё не
// нажимали. Клавиша читается из раскладки (LOOK-04), а не литералом.
func HintLeftShell() string {
	return fmt.Sprintf("вышли из шелла — нажмите %s, чтобы проверить починку на упавшем шаге", DefaultKeys().Retry.Help().Key)
}

// HintHandingTerminal — терминал передаётся контейнеру: последний полный
// кадр перед уходом механизма передачи терминала (09-UI-SPEC.md, «Вход и
// выход», пункт 2).
func HintHandingTerminal() string {
	return "передаю терминал контейнеру…"
}

// HintPullingImage — тяга образа началась, число слоёв ещё не известно.
func HintPullingImage(image string) string {
	return fmt.Sprintf("тяну образ %s…", Plain(image))
}

// HintCanceling — Ctrl-C во время долгой операции: секунда до возврата на
// предыдущее устойчивое состояние, без клейма ошибки.
func HintCanceling() string {
	return "отменяю…"
}

// Формулировки петли фикса (задача 3, FIXUI-01, FIXUI-02): по одной
// именованной функции на строку таблицы контракта, в порядке прохождения
// петли — починил, догнал, проверил начисто.

// HintStepFixed — R/:R починил шаг: теперь проходит.
func HintStepFixed(step int) string {
	return fmt.Sprintf("шаг %d починен — :rest прогонит оставшиеся шаги", step)
}

// HintStepStillFails — R/:R — шаг всё ещё падает.
func HintStepStillFails(step int) string {
	return fmt.Sprintf("шаг %d всё ещё падает — s, чтобы вернуться в контейнер", step)
}

// HintRestPassed — :rest — оставшиеся шаги прошли.
func HintRestPassed() string {
	return "все шаги пройдены — :clean проверит начисто в свежем контейнере"
}

// HintRestFailed — :rest — что-то упало дальше.
func HintRestFailed(step int) string {
	return fmt.Sprintf("шаг %d упал при прогоне оставшихся — вернитесь в контейнер (s)", step)
}

// HintCleanGreen — :clean — чистый прогон зелёный. Клавиша читается из
// раскладки (LOOK-04), а не литералом.
func HintCleanGreen() string {
	return fmt.Sprintf("чистый прогон зелёный — нажмите %s, чтобы перенести правки в свой репозиторий", DefaultKeys().Apply.Help().Key)
}

// HintCleanFails — :clean — чистый прогон падает.
func HintCleanFails(step int) string {
	return fmt.Sprintf("чистый прогон падает на шаге %d — вернитесь в контейнер и проверьте ещё раз", step)
}

// HintRunning — долгая операция (перезапуск, :rest, :clean) идёт: what —
// метка вида прогона (RunLabelRest/RunLabelClean или собственная строка
// перезапуска шага), step/total — прогресс по маркерам прогона шагов.
func HintRunning(what string, step, total int) string {
	return fmt.Sprintf("выполняю %s… шаг %d из %d", what, step, total)
}

// Метки вида прогона, названные контрактом (09-UI-SPEC.md, «Долгие
// операции»).
const (
	RunLabelRest  = "rest"
	RunLabelClean = "чистый прогон"
)

// Формулировки переноса правок и фиксации (задача 2, план 09-03, FIXUI-03):
// по одной именованной функции на строку таблицы контракта, в порядке
// прохождения — список показан, перенесено, поле сообщения, зафиксировано.

// HintApplyConfirm — A/:A показал список изменённых файлов, перенос ещё не
// подтверждён.
func HintApplyConfirm(files int) string {
	return fmt.Sprintf("%d изменённых файлов — подтвердите перенос (y/n)", files)
}

// HintApplied — правки перенесены трёхсторонним мерджем, ещё не
// закоммичены.
func HintApplied(files int) string {
	return fmt.Sprintf("правки перенесены, %d файлов — :commit зафиксирует их в вашем репозитории", files)
}

// HintCommitMessage — поле ввода сообщения коммита открыто (стадия
// applyMessage). Про подтверждение текста, а не про закон ленты — клавиша
// читается confirmKey (см. выше), а не GlyphRight.
func HintCommitMessage() string {
	return "введите сообщение коммита и нажмите " + confirmKey()
}

// HintCommitted — :commit выполнен успешно. Клавиша читается из раскладки
// (LOOK-04), а не литералом.
func HintCommitted() string {
	return fmt.Sprintf("зафиксировано — %s для выхода или продолжайте чинить следующий шаг", DefaultKeys().Quit.Help().Key)
}

// HintNoChanges — A/:A нажаты, но в воспроизведённом чекауте нет правок.
// Формулировка переезжает из обычного режима переноса дословно, чтобы голос
// продукта не раздваивался.
func HintNoChanges() string {
	return "правок в чекауте нет — переносить нечего"
}

// HintImageReplaced — :image заменил образ и перезапустил контейнер
// (задача 3, план 09-03, FIXUI-05). Формулировка дословно из раздела
// «Copywriting Contract» контракта — блокирующего вопроса у замены нет,
// потому что она обратима повторной командой.
func HintImageReplaced(image string) string {
	return fmt.Sprintf("образ заменён на %s, контейнер перезапущен", Plain(image))
}

// HintExecDone — :!<команда> завершилась кодом code. Формулировка не задана
// контрактом (произвольная команда в его таблицах не разобрана) — собрана по
// тем же правилам: называет следующий шаг (посмотреть вывод), а не просто
// констатирует код.
func HintExecDone(code int) string {
	return fmt.Sprintf("команда завершилась с кодом %d — вывод в панели лога", code)
}

// Формулировки экрана дерева групп и проектов (Фаза 10, BROW-01,
// 09-UI-SPEC.md «Строка подсказки»): по одной именованной функции на строку
// таблицы, чтобы каждая формулировка жила ровно в одном месте.

// HintTreeLoading — идёт первая загрузка корня дерева.
func HintTreeLoading() string {
	return "тяну список групп и проектов…"
}

// HintTreeExpand — курсор на свёрнутой группе. Закон ленты делает клавишу
// подтверждения синонимом движения вправо (Фаза 13, idea-0.3.1 §3) —
// раскрытие узла тоже «глубже», хоть и внутри той же колонки репозиториев,
// а не переход в колонку правее; подсказка называет основную клавишу
// закона, а не клавишу подтверждения.
func HintTreeExpand(name string) string {
	return fmt.Sprintf("группа %s свёрнута — "+GlyphRight+" раскроет её", name)
}

// HintTreeCollapse — курсор на раскрытой группе.
func HintTreeCollapse(name string) string {
	return fmt.Sprintf(GlyphRight+" свернёт группу %s", name)
}

// HintTreeOpenProject — курсор на проекте: → откроет колонку пайплайнов
// правее — настоящий переход в колонку, а не раскрытие внутри той же.
func HintTreeOpenProject(name string) string {
	return fmt.Sprintf(GlyphRight+" покажет пайплайны проекта %s", name)
}

// HintTreeEmpty — токену не видно ни одной группы и ни одного проекта.
func HintTreeEmpty() string {
	return "токену не видно ни одной группы и ни одного проекта"
}

// HintTreeFailed — загрузка дерева провалилась. Клавиша читается из
// раскладки (LOOK-04), а не литералом.
func HintTreeFailed(reason string) string {
	return fmt.Sprintf("не удалось получить список: %s — %s повторит запрос", reason, DefaultKeys().Refresh.Help().Key)
}

// HintTreeFiltered — включён фильтр по дереву. Клавиша читается из
// раскладки (LOOK-04), а не литералом.
func HintTreeFiltered(n int) string {
	return fmt.Sprintf("подходит записей: %d — %s снимет фильтр", n, DefaultKeys().Back.Help().Key)
}

// HintTreeStale — показанный список дерева взят из кэша. Клавиша читается
// из раскладки (LOOK-04), а не литералом.
func HintTreeStale(age string) string {
	return fmt.Sprintf("список из кэша, %s назад — %s обновит", age, DefaultKeys().Refresh.Help().Key)
}

// Формулировки корня-хоста дерева репозиториев (Фаза 14, план 14-02,
// MENU-03): каждый ключ — свой корень, и три состояния строки-хоста
// (свёрнут, раскрыт, доступ ещё запрашивается) нуждаются в собственных
// формулировках, тем же приёмом, что и весь остальной файл.

// HintTreeHostExpand — курсор на свёрнутом корне-хосте, доступ к которому
// уже есть.
func HintTreeHostExpand(host string) string {
	return fmt.Sprintf(GlyphRight+" покажет группы и проекты %s", host)
}

// HintTreeHostCollapse — курсор на раскрытом корне-хосте.
func HintTreeHostCollapse(host string) string {
	return fmt.Sprintf(GlyphRight+" свернёт %s", host)
}

// HintTreeHostOpening — доступ к хосту сейчас запрашивается: просьба уже
// ушла корневой модели (needHostMsg), ответ (hostReadyMsg) ещё не пришёл.
func HintTreeHostOpening(host string) string {
	return fmt.Sprintf("спрашиваю, кто вы на %s…", host)
}

// HintTreeNoKeys — перечень хостов пуст. Попасть на этот экран без ключей
// нельзя: пункт репозиториев меню недоступен, пока перечень пуст
// (menuModel.enabled) — ветвь существует ради честности пустого состояния, а
// не ради достижимого пути. esc с самой левой колонки ленты завершает
// программу (закон ленты, Фаза 13) — формулировка называет это прямо, а не
// обещает возврат в меню, которого отсюда нет.
// Клавиша читается из раскладки (LOOK-04), а не литералом.
func HintTreeNoKeys() string {
	return fmt.Sprintf("ключей пока нет — %s выйдет из утилиты", DefaultKeys().Back.Help().Key)
}

// NoteSourceEmpty и NoteSourceRefused — две формулировки, ради которых
// строка-объяснение и заведена (internal/ui/tree.go, treeKindNote). Пустой
// ответ и отказ источника — РАЗНЫЕ события, и на экране они обязаны
// выглядеть по-разному: пока обе ситуации показывались одинаковой пустотой,
// разбираться с пустым списком проектов приходилось гаданием, а не чтением
// экрана. Адрес запроса в отказ подставлять здесь не нужно — он уже внутри
// reason: каждая ошибка провайдера списков несёт путь, который спрашивался
// (internal/provider/gitlab/browse.go).

// NoteSourceEmpty — источник ответил успешно и не отдал ни одной записи.
func NoteSourceEmpty() string {
	return "источник вернул ноль записей"
}

// NoteSourceRefused — источник ответил отказом; reason несёт причину вместе
// с адресом, который запрашивался.
func NoteSourceRefused(reason string) string {
	return fmt.Sprintf("источник ответил отказом: %s", reason)
}

// HintSplashExit — единственная формулировка выхода с заставки. Своей
// строки клавиш у заставки нет (кадр собирает App.viewInner, и заставка —
// единственный экран без неё), поэтому названа она здесь, а не в KeyBar. С
// Фазы 14 esc уводит в меню, а не завершает программу — заставка больше не
// единственная точка входа, вернуться отсюда есть куда; выход по-прежнему
// живёт на клавише выхода и на Ctrl-C, которую перехватывает корневая
// модель.
// Клавиши читаются из раскладки (LOOK-04), а не литералом.
func HintSplashExit() string {
	return fmt.Sprintf(
		"%s снимет набранное, ещё раз — в меню; %s и %s выходят сразу",
		DefaultKeys().Back.Help().Key, DefaultKeys().Quit.Help().Key, DefaultKeys().Cancel.Help().Key,
	)
}

// HintTreeNote — курсор стоит на строке-объяснении. Клавиша читается из
// раскладки (LOOK-04), а не литералом.
func HintTreeNote(text string) string {
	return fmt.Sprintf("%s — %s повторит запрос", text, DefaultKeys().Refresh.Help().Key)
}

// Формулировки правой панели пайплайнов экрана дерева (Фаза 10, BROW-03).

// HintPipelinesPick — проект ещё не выбран.
func HintPipelinesPick() string {
	return "выберите проект слева, чтобы увидеть его пайплайны"
}

// HintPipelinesLoading — идёт загрузка пайплайнов проекта project.
func HintPipelinesLoading(project string) string {
	return fmt.Sprintf("тяну пайплайны проекта %s…", project)
}

// HintPipelinesEmpty — в проекте project ещё не было пайплайнов.
func HintPipelinesEmpty(project string) string {
	return fmt.Sprintf("в проекте %s ещё не было пайплайнов", project)
}

// HintPipelinesFailed — загрузка пайплайнов провалилась. Клавиша читается
// из раскладки (LOOK-04), а не литералом.
func HintPipelinesFailed(reason string) string {
	return fmt.Sprintf("не удалось получить пайплайны: %s — %s повторит запрос", reason, DefaultKeys().Refresh.Help().Key)
}

// HintPipelineOpen — курсор на строке пайплайна iid: → откроет колонку джоб
// правее (закон ленты, Фаза 13).
func HintPipelineOpen(iid int64) string {
	return fmt.Sprintf(GlyphRight+" покажет джобы пайплайна #%d", iid)
}

// Формулировки панели лога джобы настоящего прогона (Фаза 10, BROW-04).
// Панель показывает лог, полученный от API через клиент обхода — не то же
// самое, что панель лога локального воспроизведения (internal/ui/job.go),
// и общих формулировок у них нет.

// HintLogLoading — идёт запрос лога.
func HintLogLoading() string {
	return "тяну лог упавшего шага…"
}

// HintLogTail — показан хвост лога секции section.
func HintLogTail(section string) string {
	return fmt.Sprintf("показан хвост секции %s — :log вытянет лог целиком", section)
}

// HintLogFull — показан полный лог. esc здесь означает то же, что и везде
// после Фазы 13 — уход в самую левую колонку ленты, а не «назад к списку
// джоб»: список джоб — не всегда самая левая колонка (вход из браузера
// групп и проектов кладёт репозитории левее него), и старая формулировка
// после ленты стала бы враньём в этом случае.
func HintLogFull() string {
	return fmt.Sprintf("лог целиком — ↑↓ прокрутит его, %s уйдёт в начало ленты", DefaultKeys().Back.Help().Key)
}

// HintLogUnavailable — запрос лога провалился. Клавиша читается из
// раскладки (LOOK-04), а не литералом.
func HintLogUnavailable(reason string) string {
	return fmt.Sprintf("лог джобы недоступен: %s — %s повторит запрос", reason, DefaultKeys().Refresh.Help().Key)
}

// Формулировки полноэкранного просмотрщика лога (Фаза 15, план 15-01,
// LOG-01…LOG-04): по одной именованной функции на состояние просмотрщика,
// тем же приёмом, что и весь остальной файл. Формулировки панели лога выше
// (HintLogTail, HintLogFull, HintLogLoading, HintLogUnavailable) не
// трогаются — они про состояние ЗАГРУЗКИ лога, а не про его чтение.

// HintLogViewing — обычный просмотр: searchKey откроет поиск, failedKey
// уведёт к упавшему шагу. Обе клавиши читаются из раскладки вызывающим
// кодом (internal/ui/logview.go, hintText) — здесь только формулировка.
func HintLogViewing(searchKey, failedKey string) string {
	return fmt.Sprintf("%s начнёт поиск, %s уведёт к упавшему шагу", searchKey, failedKey)
}

// HintLogSearching — поле поиска открыто: подтверждение покажет совпадения,
// возврат снимет поиск целиком.
func HintLogSearching() string {
	return "введите запрос и нажмите " + confirmKey() + " — " + DefaultKeys().Back.Help().Key + " снимет поиск"
}

// HintLogFound — поиск задан, совпадения есть: nextKey переходит к
// следующему.
func HintLogFound(n int, nextKey string) string {
	return fmt.Sprintf("совпадений: %d — %s к следующему", n, nextKey)
}

// HintLogNothingFound — поиск задан, совпадений нет.
func HintLogNothingFound() string {
	return "совпадений нет"
}

// HintLogEmpty — лог пуст, читать нечего.
func HintLogEmpty() string {
	return "лог пуст — здесь появится вывод шага"
}

// Формулировки проводника по поломкам (Фаза 11, GUIDE-01…05): по одной
// именованной функции на ситуацию словаря internal/ui/guide.go — тот же
// приём, что и у остальных строк подсказки этого файла.

// HintTokenNeeded — экран проводника: токена для хоста нет. Про
// подтверждение поля ввода проводника, а не про закон ленты — confirmKey, а
// не GlyphRight (проводник — оверлей, закону стрелок не подчиняется).
func HintTokenNeeded(host string) string {
	return fmt.Sprintf("токен для %s не найден — введите его здесь, "+confirmKey()+" сохранит и повторит проверку", host)
}

// HintTokenRejected — экран проводника: хост отклонил токен.
func HintTokenRejected(host string) string {
	return fmt.Sprintf("%s отклонил токен — вставьте новый, "+confirmKey()+" сохранит и повторит проверку", host)
}

// HintTokenSaved — токен сохранён на диск, проверка повторяется.
func HintTokenSaved(path string) string {
	return fmt.Sprintf("токен сохранён в %s — повторяю проверку", path)
}

// HintTokenNotSaved — не удалось сохранить токен на диск, но запуск не
// прерывается: токен используется только в этом прогоне.
func HintTokenNotSaved() string {
	return "токен не сохранён на диск — использую только в этом запуске"
}

// HintTokenScope — экран проводника: токену не хватает области
// read_repository.
func HintTokenScope(host string) string {
	return fmt.Sprintf("токен не даёт доступ к коду — допишите read_repository в настройках %s и вставьте токен здесь", host)
}

// HintFixPerms — экран проводника с готовой командой chmod.
func HintFixPerms() string {
	return "выполните команду ниже и нажмите " + confirmKey() + ", чтобы повторить"
}

// HintDockerMissing — экран проводника: Docker не найден.
func HintDockerMissing() string {
	return "Docker не найден — выполните команду ниже, поставьте его и нажмите " + confirmKey()
}

// HintDaemonDown — экран проводника: демон Docker не отвечает.
func HintDaemonDown() string {
	return "демон Docker не отвечает — запустите его и нажмите " + confirmKey()
}

// HintCacheHidden — экран проводника: демон не видит каталог данных,
// предлагается переключить его на dir.
func HintCacheHidden(dir string) string {
	return fmt.Sprintf("snap-докер не видит скрытые каталоги — переключить данные в %s? (y/n)", dir)
}

// HintDataDirNeeded — экран проводника: каталог данных не вычислим или
// негоден, нужен путь руками.
func HintDataDirNeeded() string {
	return "введите абсолютный путь к каталогу данных — он сохранится в настройках"
}

// HintDataDirSaved — каталог данных сохранён, подготовка повторяется.
func HintDataDirSaved(dir string) string {
	return fmt.Sprintf("каталог данных теперь %s — повторяю подготовку", dir)
}

// HintSecretsMissing — экран проводника: не хватает n значений секретов.
func HintSecretsMissing(n int) string {
	return fmt.Sprintf("не хватает %d значений — "+confirmKey()+" откроет файл секретов в редакторе", n)
}

// HintSecretsEdited — редактор секретов закрыт, права возвращены, подготовка
// повторяется.
func HintSecretsEdited(path string) string {
	return fmt.Sprintf("права файла %s возвращены к 0600 — повторяю подготовку", path)
}

// HintSecretsFailed — редактор секретов не открылся.
func HintSecretsFailed(reason string) string {
	return fmt.Sprintf("редактор не открылся: %s", reason)
}

// HintSSHKey — экран проводника: git просит ключ доступа.
func HintSSHKey(host string) string {
	return fmt.Sprintf("git просит ключ — заведите его командами ниже, добавьте на %s и нажмите "+confirmKey(), host)
}

// HintPullDone — :pull обновил рабочую копию успешно.
func HintPullDone() string {
	return "рабочая копия обновлена — продолжайте чинить"
}

// HintPullFailed — :pull отказал.
func HintPullFailed(reason string) string {
	return fmt.Sprintf("обновить рабочую копию не вышло: %s", reason)
}

// HintUnresolvedConfig — экран проводника: конфиг использует extends,
// образ не разобран.
func HintUnresolvedConfig() string {
	return "конфиг использует extends — образ не разобран, задайте его здесь, шаги останутся неразобранными"
}

// HintImageSet — образ задан экраном проводника, подготовка повторяется.
func HintImageSet(image string) string {
	return fmt.Sprintf("образ %s задан — повторяю подготовку", Plain(image))
}

// HintApplyOutsideRepo — экран проводника: перенос правок вне рабочей копии.
func HintApplyOutsideRepo(path string) string {
	return fmt.Sprintf("правки сохранены в %s — перейдите в рабочую копию проекта и повторите перенос", path)
}

// HintNoPatchYet — экран проводника: сохранённых правок ещё нет.
func HintNoPatchYet() string {
	return "правок пока нет — почините шаг в контейнере, потом переносите"
}

// HintEnvScreen — полноэкранное окружение джобы (:env), n переменных.
func HintEnvScreen(n int) string {
	return fmt.Sprintf("окружение джобы, %d переменных — %s вернёт на экран джобы", n, DefaultKeys().Back.Help().Key)
}

// Формулировки закона ленты колонок (Фаза 13, NAV-01…NAV-02): по одной
// именованной функции на ситуацию, тем же приёмом, что и все остальные
// формулировки этого файла. Клавиша называется не литералом, а отображаемой
// формой привязки — GlyphRight/GlyphLeft те же константы единственной
// таблицы символов темы (internal/ui/theme.go), которыми названы сами
// привязки Right/Left в раскладке (internal/ui/keys.go), поэтому текст не
// может разойтись с раскладкой.

// HintColumnDeeper — что откроется по стрелке вправо на текущем элементе.
func HintColumnDeeper(what string) string {
	return fmt.Sprintf(GlyphRight+" откроет %s", what)
}

// HintColumnLeftmost — человек в самой левой колонке ленты: esc отсюда
// выходит из утилиты — единственное место, где esc завершает программу, и
// формулировка обязана назвать это прямо.
func HintColumnLeftmost() string {
	return DefaultKeys().Back.Help().Key + " выйдет из утилиты"
}

// HintColumnDeepest — правее фокуса в ленте ничего нет, ← вернёт назад.
func HintColumnDeepest() string {
	return GlyphLeft + " вернёт назад"
}

// Формулировки меню входа (Фаза 14, MENU-01…MENU-05): по одной именованной
// функции на формулировку, тем же приёмом, что и весь остальной файл — ни
// одной строки, собранной по месту.

// HintMenuNoKeys — ключей нет вовсе.
func HintMenuNoKeys() string {
	return "ключей пока нет — добавьте ключ GitLab, чтобы увидеть свои репозитории"
}

// HintMenuNeedsKey — попытка открыть пункт, которому нужен ключ. Не отказ,
// а объяснение: человек не сделал ничего неправильного, и клеймо ошибки
// здесь было бы враньём.
func HintMenuNeedsKey() string {
	return "для этого нужен ключ — добавьте ключ GitLab, это займёт полминуты"
}

// HintMenuSoon — попытка открыть пункт GitHub: провайдера ещё нет, ключ
// принимать нечему.
func HintMenuSoon() string {
	return "GitHub — скоро; провайдера ещё нет, добавить ключ здесь нельзя"
}

// HintMenuRepos — курсор на пункте репозиториев при n доступных ключах.
func HintMenuRepos(n int) string {
	return fmt.Sprintf(GlyphRight+" откроет дерево репозиториев — доступно ключей: %d", n)
}

// HintMenuOpenLink — курсор на пункте «открыть джобу по ссылке». Оговорка
// названа прямо: метаданные джобы закрытого проекта API не отдаёт никому
// без ключа, и если проект окажется закрытым, откроется экран добавления
// ключа — обещать безусловную работу без ключа нельзя, это враньё, которое
// всплывёт на первом же приватном проекте.
func HintMenuOpenLink() string {
	return GlyphRight + " спросит ссылку на джобу — закрытый проект приведёт на экран добавления ключа"
}

// HintMenuAddKey — курсор на пункте добавления ключа известного хоста host.
func HintMenuAddKey(host string) string {
	return fmt.Sprintf(GlyphRight+" откроет экран добавления ключа для %s", host)
}

// HintMenuAddHost — курсор на пункте своего инстанса: сначала спрашивается
// адрес, потом ключ.
func HintMenuAddHost() string {
	return GlyphRight + " спросит адрес вашего инстанса, затем ключ"
}

// HintMenuKeyInput — поле ввода ключа открыто (Фаза 14, задача 2).
// Формулировка называет ту же честность, ради которой ввод сделан скрытым:
// ключ не отображается при вводе и сохранится в файл с правами только для
// владельца. Ни одна из формулировок ниже не принимает значение ключа
// параметром — это инвариант, а не аккуратность: строка подсказки уходит в
// кадр целиком.
func HintMenuKeyInput(host string) string {
	return fmt.Sprintf("ключ для %s не отображается при вводе и сохранится в файл с правами только для владельца", host)
}

// HintMenuHostInput — поле адреса инстанса открыто, следующим шагом будет
// ключ.
func HintMenuHostInput() string {
	return "введите адрес своего инстанса — следующим шагом будет ключ"
}

// HintMenuKeySaved — ключ сохранён, репозитории хоста host теперь видны.
func HintMenuKeySaved(host string) string {
	return fmt.Sprintf("ключ для %s сохранён — репозитории хоста теперь видны", host)
}

// HintMenuKeyNotSaved — сохранить ключ не вышло, с причиной reason.
func HintMenuKeyNotSaved(reason string) string {
	return fmt.Sprintf("ключ не сохранён: %s", reason)
}

// Формулировки правой колонки экрана джобы (Фаза 15, план 15-02,
// JOB-01…JOB-04): по одной именованной функции на состояние, тем же
// приёмом, что и весь остальной файл. Клавиши приходят параметрами,
// прочитанными из раскладки вызывающим — литералов клавиш здесь нет (тот же
// приём, что ввёл план 13-03).

// HintDetailNext — что сделает стрелка вправо в правой колонке: переключит
// вид на next.
func HintDetailNext(key, next string) string {
	return fmt.Sprintf("%s переключит вид на «%s»", key, next)
}

// HintSecretMissing — курсор на незаполненной скрытой переменной name: key
// впишет значение.
func HintSecretMissing(name, key string) string {
	return fmt.Sprintf("%s не задана — %s впишет значение", name, key)
}

// HintSecretFilled — курсор на уже заполненной переменной name: key
// перезапишет значение.
func HintSecretFilled(name, key string) string {
	return fmt.Sprintf("%s задана — %s перезапишет значение", name, key)
}

// HintSecretsNone — у этой джобы нет ни одной скрытой переменной.
func HintSecretsNone() string {
	return "у этой джобы нет скрытых переменных"
}

// HintSecretsFile — приглушённая строка под списком секретов: путь файла и
// напоминание, что его по-прежнему можно открыть в редакторе (Фаза 11,
// GUIDE-03 не отменяется).
func HintSecretsFile(path string) string {
	return fmt.Sprintf("файл: %s — тот же редактор командой :secrets", path)
}

// HintSecretPrompt — поле ввода значения переменной name открыто: значение
// не отображается при вводе и уйдёт в файл секретов. Ни в этой формулировке,
// ни в двух следующих значения нет и быть не может — параметрами идут
// только имя переменной, путь файла и текст ошибки домена.
func HintSecretPrompt(name string) string {
	return fmt.Sprintf("значение %s не отображается при вводе и уйдёт в файл секретов", name)
}

// HintSecretSaved — значение переменной name записано в файл path,
// подготовка повторяется.
func HintSecretSaved(name, path string) string {
	return fmt.Sprintf("значение %s записано в %s — повторяю подготовку", name, path)
}

// HintSecretFailed — записать значение не вышло, с причиной reason.
func HintSecretFailed(reason string) string {
	return fmt.Sprintf("записать не вышло: %s", reason)
}
