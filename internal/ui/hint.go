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

// Формулировки строки подсказки экрана списка джоб (09-UI-SPEC.md, «Строка
// подсказки») — по одной именованной функции на строку таблицы контракта,
// чтобы каждая формулировка жила ровно в одном месте проекта. Остальные
// строки той же таблицы — состояния петли фикса, переноса правок, коммита
// и подмены образа экрана джобы — принадлежат планам 09-02 и 09-03: таблица
// контракта разложена по планам целиком, а не урезана здесь.

// HintJobFailed — в пайплайне есть упавшая джоба, ещё не открыта, и номер
// упавшего шага известен.
func HintJobFailed(name string, step, total int) string {
	return fmt.Sprintf(
		"джоба %s упала на шаге %d из %d — нажмите "+GlyphEnter+", чтобы воспроизвести её локально",
		Plain(name), step, total,
	)
}

// HintJobFailedUnknownStep — та же ситуация, но номер упавшего шага
// неизвестен: конфиг джобы на экране списка не запрашивается. Отдельная
// формулировка, а не нули в подстановке: «упала на шаге 0 из 0» — такое же
// враньё числами, только менее заметное (WR-13 обзора v0.3.0).
func HintJobFailedUnknownStep(name string) string {
	return fmt.Sprintf(
		"джоба %s упала — нажмите "+GlyphEnter+", чтобы воспроизвести её локально",
		Plain(name),
	)
}

// HintNoFailedJob — ни одна джоба не упала (всё зелёное/ещё выполняется).
func HintNoFailedJob() string {
	return "ни одна джоба не упала — нажмите g, чтобы обновить список"
}

// HintPipelineRunning — пайплайн выполняется, есть running-джобы.
func HintPipelineRunning() string {
	return "пайплайн ещё выполняется — g обновит список"
}

// HintPipelineEmpty — список джоб пуст (0 джоб в пайплайне).
func HintPipelineEmpty() string {
	return "в этом пайплайне нет джоб"
}

// HintRetryJobs — загрузка списка джоб провалилась.
func HintRetryJobs() string {
	return "g повторит запрос"
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
func HintImageReady(image string) string {
	return fmt.Sprintf("образ %s готов — нажмите s, чтобы войти в контейнер", Plain(image))
}

// HintLeftShell — вышли из шелла (после возврата терминала), R ещё не нажимали.
func HintLeftShell() string {
	return "вышли из шелла — нажмите R, чтобы проверить починку на упавшем шаге"
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

// HintCleanGreen — :clean — чистый прогон зелёный.
func HintCleanGreen() string {
	return "чистый прогон зелёный — нажмите A, чтобы перенести правки в свой репозиторий"
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
// applyMessage).
func HintCommitMessage() string {
	return "введите сообщение коммита и нажмите ⏎"
}

// HintCommitted — :commit выполнен успешно.
func HintCommitted() string {
	return "зафиксировано — q для выхода или продолжайте чинить следующий шаг"
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

// HintTreeExpand — курсор на свёрнутой группе.
func HintTreeExpand(name string) string {
	return fmt.Sprintf("группа %s свёрнута — ⏎ раскроет её", name)
}

// HintTreeCollapse — курсор на раскрытой группе.
func HintTreeCollapse(name string) string {
	return fmt.Sprintf("⏎ свернёт группу %s", name)
}

// HintTreeOpenProject — курсор на проекте.
func HintTreeOpenProject(name string) string {
	return fmt.Sprintf("⏎ покажет пайплайны проекта %s", name)
}

// HintTreeEmpty — токену не видно ни одной группы и ни одного проекта.
func HintTreeEmpty() string {
	return "токену не видно ни одной группы и ни одного проекта"
}

// HintTreeFailed — загрузка дерева провалилась.
func HintTreeFailed(reason string) string {
	return fmt.Sprintf("не удалось получить список: %s — g повторит запрос", reason)
}

// HintTreeFiltered — включён фильтр по дереву.
func HintTreeFiltered(n int) string {
	return fmt.Sprintf("подходит записей: %d — esc снимет фильтр", n)
}

// HintTreeStale — показанный список дерева взят из кэша.
func HintTreeStale(age string) string {
	return fmt.Sprintf("список из кэша, %s назад — g обновит", age)
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

// HintPipelinesFailed — загрузка пайплайнов провалилась.
func HintPipelinesFailed(reason string) string {
	return fmt.Sprintf("не удалось получить пайплайны: %s — g повторит запрос", reason)
}

// HintPipelineOpen — курсор на строке пайплайна iid.
func HintPipelineOpen(iid int64) string {
	return fmt.Sprintf("⏎ покажет джобы пайплайна #%d", iid)
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

// HintLogFull — показан полный лог.
func HintLogFull() string {
	return "лог целиком — ↑↓ прокрутит его, esc вернёт к списку"
}

// HintLogUnavailable — запрос лога провалился.
func HintLogUnavailable(reason string) string {
	return fmt.Sprintf("лог джобы недоступен: %s — g повторит запрос", reason)
}

// Формулировки проводника по поломкам (Фаза 11, GUIDE-01…05): по одной
// именованной функции на ситуацию словаря internal/ui/guide.go — тот же
// приём, что и у остальных строк подсказки этого файла.

// HintTokenNeeded — экран проводника: токена для хоста нет.
func HintTokenNeeded(host string) string {
	return fmt.Sprintf("токен для %s не найден — введите его здесь, ⏎ сохранит и повторит проверку", host)
}

// HintTokenRejected — экран проводника: хост отклонил токен.
func HintTokenRejected(host string) string {
	return fmt.Sprintf("%s отклонил токен — вставьте новый, ⏎ сохранит и повторит проверку", host)
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
	return "выполните команду ниже и нажмите ⏎, чтобы повторить"
}

// HintDockerMissing — экран проводника: Docker не найден.
func HintDockerMissing() string {
	return "Docker не найден — выполните команду ниже, поставьте его и нажмите ⏎"
}

// HintDaemonDown — экран проводника: демон Docker не отвечает.
func HintDaemonDown() string {
	return "демон Docker не отвечает — запустите его и нажмите ⏎"
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
	return fmt.Sprintf("не хватает %d значений — ⏎ откроет файл секретов в редакторе", n)
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
	return fmt.Sprintf("git просит ключ — заведите его командами ниже, добавьте на %s и нажмите ⏎", host)
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
	return fmt.Sprintf("окружение джобы, %d переменных — esc вернёт на экран джобы", n)
}
