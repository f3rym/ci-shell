// artifacts.go — скачивание архива артефактов джобы (ART-02, ART-03,
// ART-04). Отдельный файл, а не строка в gitlab.go: у метода своя форма
// возврата — тело ответа отдаётся вызывающему ОТКРЫТЫМ, потоком, — и своя
// причина этой формы, которую стоит объяснять рядом с ним, а не в общем
// файле клиента.
package gitlab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/f3rym/ci-shell/internal/joburl"
	"github.com/f3rym/ci-shell/internal/provider"
)

// ArtifactsArchive отдаёт zip артефактов джобы потоком.
//
// Тело НЕ читается здесь и НЕ буферизуется: размер архива непредсказуем, а
// общий предел ответа API (maxResponseBytes, десятки мегабайт) рассчитан на
// json, а не на сотни мегабайт двоичных данных. Предел размера — забота
// вызывающего (internal/artifacts), потому что только он знает, куда и
// сколько готов записать; здесь возвращается объявленная длина, чтобы он
// мог отказаться, не прочитав ни байта.
//
// Закрывает тело вызывающий — тот же контракт, что у http.Response.Body, и
// та же форма, что уже описана у request.
func (c *Client) ArtifactsArchive(ctx context.Context, projectPath string, jobID int64) (io.ReadCloser, int64, error) {
	if !joburl.ValidProjectPath(projectPath) {
		return nil, 0, fmt.Errorf("gitlab: путь проекта %q недопустим: %w", projectPath, provider.ErrArtifactsUnavailable)
	}

	path := fmt.Sprintf("/projects/%s/jobs/%d/artifacts", url.PathEscape(projectPath), jobID)

	resp, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		if errors.Is(err, errStatusNotFound) {
			// GitLab отвечает 404 и на «срок хранения истёк», и на «джоба
			// не публиковала артефактов» — различить их по ответу нельзя,
			// поэтому сообщение называет обе причины сразу.
			return nil, 0, fmt.Errorf("gitlab: артефакты джобы %s#%d на хосте %s: %w", projectPath, jobID, c.host, provider.ErrArtifactsUnavailable)
		}
		if errors.Is(err, provider.ErrUnauthorized) {
			// Отказ по правам на ЭТОМ эндпоинте не обязательно значит
			// отклонённый токен: доступ к артефактам ограничивается
			// отдельной настройкой проекта. Текст уточняется, но обёртка
			// остаётся прежней sentinel-ошибкой — errors.Is у вызывающего
			// продолжает работать.
			return nil, 0, fmt.Errorf("gitlab: артефакты джобы %s#%d недоступны этому токену — проверьте ограничение доступа к артефактам в настройках проекта, а не только сам токен: %w", projectPath, jobID, provider.ErrUnauthorized)
		}
		return nil, 0, err
	}

	return resp.Body, resp.ContentLength, nil
}
