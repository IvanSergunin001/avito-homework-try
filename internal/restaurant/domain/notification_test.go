// Правило повторов проверяется без всякой инфраструктуры: сколько ждать перед следующей
// попыткой и когда попытки кончились — это арифметика, а не разговор с площадкой.
package domain_test

import (
	"testing"
	"time"

	"github.com/IvanSergunin001/Test157/internal/restaurant/domain"
)

// retryPolicy собирает правило повторов, отказываясь продолжать, если оно не собралось.
func retryPolicy(t *testing.T, attempts int, base time.Duration) domain.RetryPolicy {
	t.Helper()

	policy, err := domain.NewRetryPolicy(attempts, base)
	if err != nil {
		t.Fatalf("собрать правило повторов: %v", err)
	}

	return policy
}

func TestPauseBeforeTheNextAttemptDoublesWithEachFailure(t *testing.T) {
	t.Parallel()

	policy := retryPolicy(t, 4, time.Second)

	for failed, expected := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second} {
		delay, again := policy.NextAttempt(failed)
		if !again {
			t.Fatalf("после %d неудач повторять отказались, а попытки ещё есть", failed)
		}
		if delay != expected {
			t.Errorf("после %d неудач пауза %s, ожидали %s: коробка добивает площадку с прежней частотой",
				failed, delay, expected)
		}
	}
}

func TestAttemptsRunOutAfterTheirDeclaredNumber(t *testing.T) {
	t.Parallel()

	policy := retryPolicy(t, 3, time.Second)

	if _, again := policy.NextAttempt(2); !again {
		t.Error("после второй неудачи из трёх попыток повторять отказались")
	}
	if _, again := policy.NextAttempt(3); again {
		t.Error("после третьей неудачи из трёх попыток коробка собралась повторять снова")
	}
}

func TestPauseBeforeTheNextAttemptDoesNotGrowBeyondItsCeiling(t *testing.T) {
	t.Parallel()

	policy := retryPolicy(t, 40, time.Second)

	delay, again := policy.NextAttempt(39)
	if !again {
		t.Fatal("попытки кончились раньше объявленного числа")
	}
	if delay > time.Hour {
		t.Errorf("пауза выросла до %s: статус, доехавший через сутки, не нужен вовсе", delay)
	}
}

func TestRestaurantBoxIsNotSetUpToNeverNotifyThePlatform(t *testing.T) {
	t.Parallel()

	if _, err := domain.NewRetryPolicy(0, time.Second); err == nil {
		t.Error("правило с нулём попыток собралось: такая коробка не уведомила бы площадку ни разу")
	}
	if _, err := domain.NewRetryPolicy(3, 0); err == nil {
		t.Error("правило с нулевой паузой собралось: коробка повторяла бы доставку без передышки")
	}
}
