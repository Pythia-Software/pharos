package archive

import (
	_ "embed"
	"encoding/json"
)

// These identify immutable calculation inputs. A changed asset needs a new ID;
// these constants alone do not enable typed work or authorize a source. The
// v1 calculation ID also pins the exact per-request pricing algorithm and
// rounding; changing those semantics requires a new calculation contract.
const (
	teamsPriceBookV1         = "pharos_prices_v1"
	teamsPriceBookV1SHA256   = "806dd3ea22b14056d056a50669be723a8e3f7c3cb6305c9dbf147f940b9637ef"
	teamsCodexModelsV1       = "pharos_codex_models_v1"
	teamsCodexModelsV1SHA256 = "4e2679c605fe5c1c67424809b17b0b26935300ca7f35beaf115cf273c67bcebe"
)

//go:embed teams_contracts/prices_v1.json
var teamsPriceBookV1Bytes []byte

//go:embed teams_contracts/codex_models_v1.json
var teamsCodexModelsV1Bytes []byte

type teamsCodexModelContract struct {
	Version int               `json:"version"`
	ID      string            `json:"id"`
	Unknown string            `json:"unknown"`
	Models  map[string]string `json:"models"`
}

func teamsLoadCodexModelContract() (*teamsCodexModelContract, error) {
	if hashBytes(teamsCodexModelsV1Bytes) != teamsCodexModelsV1SHA256 || hashBytes(teamsPriceBookV1Bytes) != teamsPriceBookV1SHA256 {
		return nil, errTeamsQueryUnsupported
	}
	var v teamsCodexModelContract
	if json.Unmarshal(teamsCodexModelsV1Bytes, &v) != nil || v.Version != 1 || v.ID != teamsCodexModelsV1 || v.Unknown != "unknown" || len(v.Models) == 0 {
		return nil, errTeamsQueryUnsupported
	}
	return &v, nil
}
func (v *teamsCodexModelContract) family(model string) string {
	if v != nil {
		if family, ok := v.Models[model]; ok {
			return family
		}
	}
	return "unknown"
}
