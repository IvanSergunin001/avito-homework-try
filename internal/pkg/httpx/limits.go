package httpx

import "net/http"

// LimitRequestBody ограничивает размер входящего тела запроса.
//
// Ограничение двойное, и оба слоя нужны. Объявленную длину проверяем до обработчика:
// отказать на заголовках дешевле, чем читать мегабайты, чтобы потом их выбросить.
// Само тело оборачивается MaxBytesReader, потому что объявленной длины может не быть
// вовсе (chunked) либо она может врать — тогда чтение оборвётся на пределе, и обработчик
// увидит ошибку разбора вместо тела, которому нет конца.
//
// Отказ пишет вызывающая сторона: формат ошибки описан спецификацией поверхности, и
// общая обвязка не вправе решать, по какой схеме отвечает та или иная поверхность.
func LimitRequestBody(maxBytes int64, refuse http.HandlerFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > maxBytes {
				refuse(w, r)

				return
			}

			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

			next.ServeHTTP(w, r)
		})
	}
}
