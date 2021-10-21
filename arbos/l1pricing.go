package arbos

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/offchainlabs/arbstate/arbos/storage"
	"github.com/offchainlabs/arbstate/arbos/util"
	"math/big"
)

type L1PricingState struct {
	storage                  *storage.Storage
	defaultAggregator        common.Address
	l1GasPriceEstimate       *big.Int
	preferredAggregators     *storage.Storage
	aggregatorFixedCharges   *storage.Storage
	aggregatorAddressesToPay *storage.Storage
}

const CompressionEstimateDenominator uint64 = 1000000

var (
	initialDefaultAggregator = common.Address{} //TODO
	l1PricingStateKey        = crypto.Keccak256([]byte("Arbitrum ArbOS L1 pricing state key"))

	preferredAggregatorKey    = crypto.Keccak256Hash([]byte("Arbitrum ArbOS preferred aggregator key"))
	aggregatorFixedChargeKey  = crypto.Keccak256Hash([]byte("Arbitrum ArbOS aggregator fixed charge key"))
	aggregatorAddressToPayKey = crypto.Keccak256Hash([]byte("Arbitrum ArbOS aggregator address to pay key"))
)

const (
	defaultAggregatorAddressOffset = 0
	l1GasPriceEstimateOffset       = 1
)
const L1PricingStateSize = 2

func AllocateL1PricingState(state *ArbosState) (*L1PricingState, common.Hash) {
	newKey := state.backingStorage.UniqueKey()
	storage := state.backingStorage.Open(newKey.Bytes())
	storage.Set(util.IntToHash(defaultAggregatorAddressOffset), common.BytesToHash(initialDefaultAggregator.Bytes()))
	l1PriceEstimate := big.NewInt(1 * params.GWei)
	storage.Set(util.IntToHash(l1GasPriceEstimateOffset), common.BigToHash(l1PriceEstimate))
	return &L1PricingState{
		storage,
		initialDefaultAggregator,
		l1PriceEstimate,
		state.backingStorage.Open(preferredAggregatorKey.Bytes()),
		state.backingStorage.Open(aggregatorFixedChargeKey.Bytes()),
		state.backingStorage.Open(aggregatorAddressToPayKey.Bytes()),
	}, newKey
}

func OpenL1PricingState(key common.Hash, backingStorage *storage.Storage) *L1PricingState {
	storage := backingStorage.Open(key.Bytes())
	defaultAggregator := common.BytesToAddress(storage.Get(util.IntToHash(defaultAggregatorAddressOffset)).Bytes())
	l1GasPriceEstimate := storage.Get(util.IntToHash(l1GasPriceEstimateOffset)).Big()
	return &L1PricingState{
		storage,
		defaultAggregator,
		l1GasPriceEstimate,
		backingStorage.Open(preferredAggregatorKey.Bytes()),
		backingStorage.Open(aggregatorFixedChargeKey.Bytes()),
		backingStorage.Open(aggregatorAddressToPayKey.Bytes()),
	}
}

func (ps *L1PricingState) SetDefaultAggregator(aggregator common.Address) {
	ps.defaultAggregator = aggregator
	ps.storage.Set(util.IntToHash(defaultAggregatorAddressOffset), common.BytesToHash(aggregator.Bytes()))
}

func (ps *L1PricingState) L1GasPriceEstimateWei() *big.Int {
	return ps.l1GasPriceEstimate
}

const L1GasPriceEstimateSamplesInAverage = 25

func (ps *L1PricingState) UpdateL1GasPriceEstimate(baseFeeWei *big.Int) {
	ps.l1GasPriceEstimate = new(big.Int).Div(
		new(big.Int).Add(
			baseFeeWei,
			new(big.Int).Mul(ps.l1GasPriceEstimate, big.NewInt(L1GasPriceEstimateSamplesInAverage-1)),
		),
		big.NewInt(L1GasPriceEstimateSamplesInAverage),
	)
	ps.storage.Set(util.IntToHash(l1GasPriceEstimateOffset), common.BigToHash(ps.l1GasPriceEstimate))
}

func (ps *L1PricingState) SetPreferredAggregator(sender common.Address, aggregator common.Address) {
	ps.preferredAggregators.Set(common.BytesToHash(sender.Bytes()), common.BytesToHash(aggregator.Bytes()))
}

func (ps *L1PricingState) PreferredAggregator(sender common.Address) common.Address {
	fromTable := ps.preferredAggregators.Get(common.BytesToHash(sender.Bytes()))
	if fromTable == (common.Hash{}) {
		return ps.defaultAggregator
	} else {
		return common.BytesToAddress(fromTable.Bytes())
	}
}

func (ps *L1PricingState) SetFixedChargeForAggregatorWei(aggregator common.Address, chargeL1Gas *big.Int) {
	ps.aggregatorFixedCharges.Set(common.BytesToHash(aggregator.Bytes()), common.BigToHash(chargeL1Gas))
}

func (ps *L1PricingState) FixedChargeForAggregatorWei(aggregator common.Address) *big.Int {
	fixedChargeL1Gas := ps.aggregatorFixedCharges.Get(common.BytesToHash(aggregator.Bytes())).Big()
	return new(big.Int).Mul(fixedChargeL1Gas, ps.L1GasPriceEstimateWei())
}

func (ps *L1PricingState) SetAggregatorAddressToPay(aggregator common.Address, addr common.Address) {
	ps.aggregatorAddressesToPay.Set(common.BytesToHash(aggregator.Bytes()), common.BytesToHash(addr.Bytes()))
}

func (ps *L1PricingState) AggregatorAddressToPay(aggregator common.Address, state *ArbosState) common.Address {
	raw := ps.aggregatorAddressesToPay.Get(common.BytesToHash(aggregator.Bytes()))
	if raw == (common.Hash{}) {
		return aggregator
	} else {
		return common.BytesToAddress(raw.Bytes())
	}
}

// Compression ratio is expressed in fixed-point representation.  A value of DataWasNotCompressed corresponds to
//    a compression ratio of 1, that is, no compression.
// A value of x (for x <= DataWasNotCompressed) corresponds to compression ratio of float(x) / float(DataWasNotCompressed).
// Values greater than DataWasNotCompressed are treated as equivalent to DataWasNotCompressed.

const DataWasNotCompressed uint64 = 1000000

func (ps *L1PricingState) GetL1Charges(
	sender common.Address,
	aggregator *common.Address,
	gasForData uint64,
	compressedSizePerMillion uint64,
) *big.Int {
	if aggregator == nil {
		return big.NewInt(0)
	}
	preferredAggregator := ps.PreferredAggregator(sender)
	if preferredAggregator != *aggregator {
		return big.NewInt(0)
	}

	if compressedSizePerMillion < DataWasNotCompressed {
		gasForData = gasForData * compressedSizePerMillion / DataWasNotCompressed
	}

	chargeForBytes := new(big.Int).Mul(big.NewInt(int64(gasForData)), ps.L1GasPriceEstimateWei())
	return new(big.Int).Add(ps.FixedChargeForAggregatorWei(preferredAggregator), chargeForBytes)
}
