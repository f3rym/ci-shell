package repo

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// gitConfigCount возвращает число пар ключ/значение, уже заданных в base
// переменной GIT_CONFIG_COUNT. Побеждает последнее вхождение — так же, как
// при дедупликации окружения дочернего процесса. Нечисловое, отрицательное
// или отсутствующее значение считается нулём: пользовательские пары в этом
// случае git и сам не прочитает.
func gitConfigCount(base []string) int {
	const prefix = "GIT_CONFIG_COUNT="
	n := 0
	for _, kv := range base {
		if !strings.HasPrefix(kv, prefix) {
			continue
		}
		v, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(kv, prefix)))
		if err != nil || v < 0 {
			n = 0
			continue
		}
		n = v
	}
	return n
}

// AuthEnv возвращает копию среза base, дополненную конфигурацией git,
// переданной переменными окружения дочернего процесса. Это единственный
// чистый способ довезти токен до git, не оставив следов: адрес с учётными
// данными (https://oauth2:<токен>@host/...) осел бы в конфигурации
// репозитория, который мы монтируем в контейнер, где его прочитает любой шаг
// джобы, а форма передачи конфигурации git аргументом командной строки
// (-c http.extraHeader=...) видна в таблице процессов (idea-0.2.0 §4).
//
// Ключи привязаны к конкретному адресу — http.<адрес зеркала>.extraHeader, —
// а не заданы глобальным http.extraHeader. Глобальный ключ git применяет ко
// всякому HTTP-запросу процесса, включая запрос по редиректу: инстанс
// GitLab (или подменивший его прокси/DNS), ответивший перенаправлением на
// другой хост, получил бы вместе с ним и PRIVATE-TOKEN, и Authorization с
// живым персональным токеном. Форма http.<url>.* этого не допускает: git
// сверяет адрес запроса с адресом в ключе. Тем же адресом ограничен
// followRedirects=false — переходить по редиректам этим запросам незачем
// вовсе.
//
// GIT_CONFIG_COUNT наращивается от уже заданного пользователем значения, а
// не выставляется числом наших пар: пользователь имеет право настраивать git
// тем же способом, и затирание его пар индексами 0 и 1 молча меняло бы
// поведение git у нас под ногами. Наши пары идут после пользовательских.
//
// Первое значение — заголовок PRIVATE-TOKEN со значением секрета: тот же
// способ, которым в API ходит клиент Фазы 1 (контракт 01-SKELETON.md), и
// именно он назван в idea §4. Второе значение — заголовок Authorization со
// схемой Basic и телом из base64 по строке "oauth2:<токен>": git-endpoint
// GitLab исторически аутентифицирует персональный токен как пароль базовой
// схемы, и это самый широко поддерживаемый способ на self-hosted инстансах
// любой давности; отправить оба заголовка одним запросом дешевле, чем
// угадывать версию сервера, а верифицировать вживую этот проект не может по
// своим ограничениям (верификация — только чтением исходников). Оба
// заголовка несут один и тот же секрет одному и тому же хосту — новой
// поверхности утечки второй заголовок не создаёт. Ключ http.<url>.extraHeader
// многозначный, поэтому две пары дают два заголовка на запросе, а не
// перезапись одного другим.
//
// GIT_TERMINAL_PROMPT=0 не даёт git при отказе аутентификации уйти в диалог
// логина в терминале и подвесить процесс посреди подготовки контейнера:
// отказ должен быть ошибкой, а не зависанием.
func AuthEnv(base []string, host, projectPath, secret string) []string {
	env := make([]string, len(base), len(base)+8)
	copy(env, base)

	basic := base64.StdEncoding.EncodeToString([]byte("oauth2:" + secret))
	prefix := "http." + mirrorURL(host, projectPath) + "."

	n := gitConfigCount(base)
	add := func(key, value string) {
		env = append(env,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", n, key),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", n, value),
		)
		n++
	}

	add(prefix+"extraHeader", "PRIVATE-TOKEN: "+secret)
	add(prefix+"extraHeader", "Authorization: Basic "+basic)
	add(prefix+"followRedirects", "false")

	return append(env,
		fmt.Sprintf("GIT_CONFIG_COUNT=%d", n),
		"GIT_TERMINAL_PROMPT=0",
	)
}
