package archive

import "testing"

func TestTeamsCalculationContractIsClosed(t *testing.T) {
	contract, err := teamsLoadCodexModelContract()
	if err != nil {
		t.Fatal(err)
	}
	if contract.family("gpt-6.1-sol") != "gpt-6.1-sol" || contract.family("gpt-6.1-sol-unpublished") != "unknown" || contract.family("GPT-6.1-SOL") != "unknown" {
		t.Fatal("model contract admitted an unpublished identity")
	}
}
