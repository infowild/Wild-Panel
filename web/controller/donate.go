package controller

// DonateEntry is one crypto address the project accepts donations on.
type DonateEntry struct {
	// Chain is the ticker + network as the README writes it ("USDT-TRC20").
	Chain string
	// Address is the receiving address, shown verbatim and copied verbatim.
	Address string
}

// donateAddresses mirrors the "## Donate" section of README.md, in the same
// order. It is duplicated rather than parsed because README.md sits outside
// this package and go:embed cannot reach a parent directory — so the copy is
// pinned by TestDonateAddressesMatchReadme, which fails the build if the two
// ever drift. Edit the README and this list together; the test names the diff.
//
// Empty for now: addresses will be added later. Keep the slice empty (not nil
// vs non-empty mismatch) until then — the overview hides the Donate button
// when this list has no entries.
var donateAddresses = []DonateEntry{}
