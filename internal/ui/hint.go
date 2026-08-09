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

// HintJobFailed — в пайплайне есть упавшая джоба, ещё не открыта.
func HintJobFailed(name string, step, total int) string {
	return fmt.Sprintf(
		"джоба %s упала на шаге %d из %d — нажмите "+GlyphEnter+", чтобы воспроизвести её локально",
		name, step, total,
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

// HintImageReady — образ готов, шелл ещё не открывали.
func HintImageReady(image string) string {
	return fmt.Sprintf("образ %s готов — нажмите s, чтобы войти в контейнер", image)
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
	return fmt.Sprintf("тяну образ %s…", image)
}

// HintPullingLayers — тяга образа идёт, число слоёв уже известно.
func HintPullingLayers(image string, done, total int) string {
	return fmt.Sprintf("тяну образ %s… слой %d из %d", image, done, total)
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
