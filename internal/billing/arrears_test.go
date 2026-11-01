package billing

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ovander/tenure-api/internal/money"
	"github.com/ovander/tenure-api/internal/period"
)

func timeMonth(m int) time.Month { return time.Month(m) }

func TestArrears(t *testing.T) {
	call := func(m int, total, paid money.Cents, reminded *period.Date) Call {
		return Call{BillingDocumentID: uuid.New(), Period: p(2025, timeMonth(m)), DueOn: d(2025, timeMonth(m), 5), TotalCents: total, PaidCents: paid, RemindedOn: reminded}
	}
	sent := d(2025, 4, 10)
	calls := []Call{
		call(3, 51548, 51548, nil), // paid: not in arrears
		call(4, 94000, 40000, &sent),
		call(5, 94000, 0, nil),
		call(6, 94000, 0, nil),
	}
	v := Arrears(uuid.New(), calls, d(2025, 6, 10), 5)
	if len(v.Items) != 3 || v.OutstandingCents != 54000+94000+94000 || v.OverdueCents != 54000+94000+94000 {
		t.Fatalf("%+v", v)
	}
	april, may, june := v.Items[0], v.Items[1], v.Items[2]
	if !april.Overdue || april.DaysLate != 66 || april.ReminderDue {
		t.Errorf("april %+v", april)
	}
	if !may.ReminderDue || may.ReminderOn != d(2025, 5, 10) {
		t.Errorf("may %+v", may)
	}
	if !june.Overdue || june.DaysLate != 5 || !june.ReminderDue {
		t.Errorf("june (due day + 5 today) %+v", june)
	}
	before := Arrears(uuid.New(), calls[3:], d(2025, 6, 9), 5)
	if before.Items[0].ReminderDue || before.Items[0].DaysLate != 4 {
		t.Errorf("day before the reminder %+v", before.Items[0])
	}
	onDue := Arrears(uuid.New(), calls[3:], d(2025, 6, 5), 5)
	if onDue.Items[0].Overdue || onDue.OverdueCents != 0 || onDue.OutstandingCents != 94000 {
		t.Errorf("on the due day %+v", onDue)
	}
	over := Arrears(uuid.New(), []Call{call(7, 100, 150, nil)}, d(2025, 8, 1), 5)
	if len(over.Items) != 0 || over.OutstandingCents != 0 {
		t.Errorf("an over-paid call is not negative arrears: %+v", over)
	}
}
