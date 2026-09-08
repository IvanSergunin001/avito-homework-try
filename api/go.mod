// Шов контрактов — самостоятельный модуль: заведение, которое интегрируется с площадкой,
// импортирует сгенерированный клиент партнёрского контракта, не утаскивая за собой службы
// площадки со всеми их зависимостями.
module github.com/IvanSergunin001/Test157/api

go 1.27.0

require github.com/oapi-codegen/runtime v1.7.0

require (
	github.com/apapsch/go-jsonmerge/v2 v2.0.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
)
