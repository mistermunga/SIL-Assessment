package accounts_test

import (
	"context"
	"slices"
	"testing"

	"clinic/internal/accounts"
)

func doctorInput(username, first, last string) accounts.DoctorInput {
	in := input(username, username+"@example.com")
	in.Firstname, in.Lastname = first, last
	return accounts.DoctorInput{UserInfo: in}
}

func TestListDoctors(t *testing.T) {
	svc, pool := setup(t)
	ctx := context.Background()

	for _, d := range []accounts.DoctorInput{
		doctorInput("dr_c", "Carl", "Charlie"),
		doctorInput("dr_a", "Ann", "Alpha"),
		doctorInput("dr_b", "Bob", "Bravo"),
		doctorInput("dr_d", "Dan", "Delta"),
	} {
		if _, err := svc.RegisterDoctor(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.RegisterPatient(ctx, input("pat", "pat@example.com")); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE users SET deleted_at = now() WHERE username = 'dr_b'"); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"dr_a", "dr_b", "dr_c"} {
		_, err := pool.Exec(ctx, `INSERT INTO doctor_schedules (doctor_id, weekday, start_time, end_time)
		SELECT user_id, 1, '09:00', '17:00' FROM users WHERE username = $1`, name)
		if err != nil {
			t.Fatal(err)
		}
	}

	list, err := svc.ListDoctors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range list {
		names = append(names, d.Name)
	}
	// patients and deleted doctors are excluded, ordered by last name
	if want := []string{"Ann Alpha", "Carl Charlie"}; !slices.Equal(names, want) {
		t.Errorf("doctors = %v, want %v", names, want)
	}
}
