package main

import "testing"

func TestStockRequest(t *testing.T) {
	cases := []struct {
		b    bcb
		want bool
	}{
		{bcb{}, false},
		{bcb{Command: "boot-recovery", Recovery: "recovery\n"}, false},
		{bcb{Command: "boot-fastboot", Recovery: "recovery\n--fastboot\n"}, true},
		{bcb{Command: "boot-fastboot"}, true},
		{bcb{Command: "boot-recovery", Recovery: "recovery\n--wipe_data\n--reason=MasterClearConfirm\n"}, true},
		{bcb{Command: "boot-recovery", Recovery: "recovery\n--update_package=/data/ota/update.zip\n"}, true},
	}
	for _, c := range cases {
		if _, got := stockRequest(c.b); got != c.want {
			t.Errorf("%+v: got %v want %v", c.b, got, c.want)
		}
	}
}

func TestWantsFastboot(t *testing.T) {
	cases := []struct {
		b    bcb
		want bool
	}{
		{bcb{}, false},
		{bcb{Command: "boot-fastboot"}, true},
		{bcb{Command: "boot-recovery", Recovery: "recovery\n--fastboot\n"}, true},
		{bcb{Command: "boot-recovery", Recovery: "recovery\n--wipe_data\n"}, false},
		{bcb{Command: "boot-recovery", Recovery: "recovery\n--fastbootx\n"}, false},
	}
	for _, c := range cases {
		if got := wantsFastboot(c.b); got != c.want {
			t.Errorf("%+v: got %v want %v", c.b, got, c.want)
		}
	}
}
