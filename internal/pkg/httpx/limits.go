package httpx

import "net/http"

// MB — мегабайт в байтах. Пределы на размер тела задаются в мегабайтах, а `1 << 20`
// в объявлении константы читается хуже, чем `1 * httpx.MB`.
const MB = 1 << 20

// LimitRequestBody ограничивает размер входящего тела запроса.
//
// Ограничение двойное, и оба слоя нужны. Объявленную длину проверяем до обработчика:
// отказать на заголовках дешевле, чем читать мегабайты, чтобы потом их выбросить.
// Само тело оборачивается MaxBytesReader, потому что объявленной длины может не быть
// вовсе (chunked) либо она может врать — тогда чтение оборвётся на пределе, и обработчик
// увидит ошибку разбора вместо тела, которому нет конца.
//
// Обе проверки заканчиваются одним и тем же отказом — тем, что пишет `refuse`: клиенту
// незачем разбирать, объявил он длину честно или прислал тело без длины вовсе, причина
// отказа у него одна. Ошибку `MaxBytesReader` сводит к этому отказу тот, кто разбирает
// тело: до middleware она уже не поднимается.
//
// Отказ пишет вызывающая сторона: формат ошибки описан спецификацией поверхности, и
// общая обвязка не вправе решать, по какой схеме отвечает та или иная поверхность.
//
// Методов без тела предел не касается: контракт тела у них не объявляет, а значит, и
// ответа на слишком большое тело у таких операций в спецификации нет.
func LimitRequestBody(maxBytes int64, refuse http.HandlerFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodDelete, http.MethodOptions:
				next.ServeHTTP(w, r)

				return
			}

			if r.ContentLength > maxBytes {
				refuse(w, r)

				return
			}

			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

			next.ServeHTTP(w, r)
		})
	}
}
