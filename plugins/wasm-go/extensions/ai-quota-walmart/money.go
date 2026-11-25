package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"github.com/tidwall/gjson"
	"math/big"
	"strings"
)

//go:embed money_ledger.lua
var moneyScript string

const moneyPolicy = "personal_department_money"
const defaultMoneyTiers = `{"L1":{"quota":500000000,"models":[]},"L2":{"quota":1500000000,"models":[]}}`

func moneyCost(input, output, inPrice, outPrice int64) (int64, error) {
	for _, n := range []int64{input, output, inPrice, outPrice} {
		if n < 0 || n > 9999999999999 {
			return 0, fmt.Errorf("pricing operand outside safe range")
		}
	}
	if input > 9999999999999-output {
		return 0, fmt.Errorf("combined token usage exceeds safe range")
	}
	n := new(big.Int).Mul(big.NewInt(input), big.NewInt(inPrice))
	n.Add(n, new(big.Int).Mul(big.NewInt(output), big.NewInt(outPrice)))
	n.Add(n, big.NewInt(999999))
	n.Div(n, big.NewInt(1000000))
	if !n.IsInt64() || n.Int64() > 9999999999999 {
		return 0, fmt.Errorf("money overflow")
	}
	return n.Int64(), nil
}
func validatePrices(raw string) error {
	var prices map[string]map[string]json.Number
	if json.Unmarshal([]byte(raw), &prices) != nil || prices == nil {
		return fmt.Errorf("prices must be an object")
	}
	for model, p := range prices {
		if !validModel(model) || len(p) != 2 {
			return fmt.Errorf("invalid price")
		}
		for _, k := range []string{"input_price_micros", "output_price_micros"} {
			n, ok := p[k]
			if !ok || gjson.Parse(raw).Map()[model].Map()[k].Type != gjson.Number || !validAmount(string(n)) || strings.HasPrefix(string(n), "-") {
				return fmt.Errorf("invalid price amount")
			}
		}
	}
	return nil
}
