package client

import "testing"

func TestResolveChestLogActionUsesConfiguredMapping(t *testing.T) {
	oldDeposit := ConfigGlobal.ChestLogDepositFilterValue
	oldWithdraw := ConfigGlobal.ChestLogWithdrawFilterValue
	defer func() {
		ConfigGlobal.ChestLogDepositFilterValue = oldDeposit
		ConfigGlobal.ChestLogWithdrawFilterValue = oldWithdraw
	}()

	ConfigGlobal.ChestLogDepositFilterValue = 7
	ConfigGlobal.ChestLogWithdrawFilterValue = 3

	recordChestLogRequestFilter(101, 7)
	filter, action := resolveChestLogAction(101)
	if filter != 7 || action != "deposit" {
		t.Fatalf("deposit mapping = (%d, %q), want (7, deposit)", filter, action)
	}

	recordChestLogRequestFilter(102, 3)
	filter, action = resolveChestLogAction(102)
	if filter != 3 || action != "withdraw" {
		t.Fatalf("withdraw mapping = (%d, %q), want (3, withdraw)", filter, action)
	}
}

func TestResolveChestLogActionMarksUnknownFilters(t *testing.T) {
	oldDeposit := ConfigGlobal.ChestLogDepositFilterValue
	oldWithdraw := ConfigGlobal.ChestLogWithdrawFilterValue
	defer func() {
		ConfigGlobal.ChestLogDepositFilterValue = oldDeposit
		ConfigGlobal.ChestLogWithdrawFilterValue = oldWithdraw
	}()

	ConfigGlobal.ChestLogDepositFilterValue = 28
	ConfigGlobal.ChestLogWithdrawFilterValue = 1

	recordChestLogRequestFilter(201, 99)
	filter, action := resolveChestLogAction(201)
	if filter != 99 || action != "filter_unknown" {
		t.Fatalf("unknown mapping = (%d, %q), want (99, filter_unknown)", filter, action)
	}
}
