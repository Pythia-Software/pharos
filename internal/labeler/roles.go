package labeler

import (
	"fmt"
	"maps"
	"slices"
	"sync"
)

// Input roles are registered: each says how Pharos fills a row of its
// tensor from a framed item (Fill) and which values a run request may carry
// (Validate). Pharos fills, workers validate, both from this one table, so a
// model whose input contract adds a role (a sentence-segment input, say)
// brings a filler and a validator here and changes nothing in the protocol,
// the batcher or the workers' loops.

// Role describes one input role.
type Role struct {
	// Fill writes one row of the role's tensor: row is the framed item,
	// values its zeroed row of the padded width.
	Fill func(row Framed, values []int32)
	// Validate checks the role's whole tensor in a run request.
	Validate func(check RoleCheck) error
	// Required roles appear exactly once in every input contract.
	Required bool
}

// RoleCheck is what a validator sees: the tensor's values and the attention
// mask's, row-major [Batch, Seq], and the hello's vocabulary size.
type RoleCheck struct {
	Name              string
	Values, Attention []int32
	Batch, Seq        int
	Vocab             int
}

var (
	rolesMu sync.RWMutex
	roles   = map[string]Role{
		RoleIDs: {Required: true,
			Fill: func(row Framed, values []int32) {
				copy(values, row.IDs)
				for col := len(row.IDs); col < len(values); col++ {
					values[col] = PadID
				}
			},
			Validate: func(c RoleCheck) error {
				for k, id := range c.Values {
					if id < 0 || c.Vocab > 0 && int(id) >= c.Vocab {
						return fmt.Errorf("token id %d at [%d,%d] is outside [0,%d)", id, k/c.Seq, k%c.Seq, c.Vocab)
					}
				}
				return nil
			}},
		RoleAttention: {Required: true,
			Fill: func(row Framed, values []int32) {
				for col := range row.IDs {
					values[col] = 1
				}
			},
			Validate: AllOf(ValuesIn(0, 1), NonEmptyRows)},
		// pool: 1 on the item's text (target) span.
		RolePool: {
			Fill: func(row Framed, values []int32) {
				for col := row.Spans.Text.Start; col < row.Spans.Text.End; col++ {
					values[col] = 1
				}
			},
			Validate: AllOf(ValuesIn(0, 1), ZeroWithoutAttention, NonEmptyRows)},
	}
)

// RegisterRole adds a role (or replaces one), for a model whose input
// contract needs it. Register it in the main process and in each worker
// that validates with ValidateRun.
func RegisterRole(name string, role Role) {
	rolesMu.Lock()
	defer rolesMu.Unlock()
	roles[name] = role
}

func lookupRole(name string) (Role, bool) {
	rolesMu.RLock()
	defer rolesMu.RUnlock()
	role, ok := roles[name]
	return role, ok
}

func requiredRoles() []string {
	rolesMu.RLock()
	defer rolesMu.RUnlock()
	names := []string{}
	for name, role := range roles {
		if role.Required {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// Roles lists the registered roles.
func Roles() []string {
	rolesMu.RLock()
	defer rolesMu.RUnlock()
	return slices.Sorted(maps.Keys(roles))
}

// AllOf runs validators in order.
func AllOf(validators ...func(RoleCheck) error) func(RoleCheck) error {
	return func(c RoleCheck) error {
		for _, validate := range validators {
			if err := validate(c); err != nil {
				return err
			}
		}
		return nil
	}
}

// ValuesIn accepts values in [lo, hi].
func ValuesIn(lo, hi int32) func(RoleCheck) error {
	return func(c RoleCheck) error {
		for k, v := range c.Values {
			if v < lo || v > hi {
				return fmt.Errorf("%s value %d at [%d,%d] is outside [%d,%d]", c.Name, v, k/c.Seq, k%c.Seq, lo, hi)
			}
		}
		return nil
	}
}

// ZeroWithoutAttention requires 0 wherever the attention mask is 0
// (padding).
func ZeroWithoutAttention(c RoleCheck) error {
	for k, v := range c.Values {
		if v != 0 && c.Attention[k] == 0 {
			return fmt.Errorf("%s is %d on padding at [%d,%d]", c.Name, v, k/c.Seq, k%c.Seq)
		}
	}
	return nil
}

// NonEmptyRows requires a non-zero value in every row.
func NonEmptyRows(c RoleCheck) error {
	for row := 0; row < c.Batch; row++ {
		if !slices.ContainsFunc(c.Values[row*c.Seq:(row+1)*c.Seq], func(v int32) bool { return v != 0 }) {
			return fmt.Errorf("row %d of %s is empty", row, c.Name)
		}
	}
	return nil
}

// ContiguousRuns requires each non-zero value to occupy one contiguous run
// of positions in its row (a span id appears once).
func ContiguousRuns(c RoleCheck) error {
	for row := 0; row < c.Batch; row++ {
		seen := map[int32]bool{}
		previous := int32(0)
		for col, v := range c.Values[row*c.Seq : (row+1)*c.Seq] {
			if v != 0 && v != previous {
				if seen[v] {
					return fmt.Errorf("%s value %d at [%d,%d] starts a second run", c.Name, v, row, col)
				}
				seen[v] = true
			}
			previous = v
		}
	}
	return nil
}
