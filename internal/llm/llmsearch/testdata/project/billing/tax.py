"""Sales tax rates by region."""

RATES = {
    "CA": 0.0725,
    "NY": 0.04,
    "TX": 0.0625,
    "WA": 0.065,
}


def rate_for(region):
    """The sales tax rate of a region, or zero where none applies."""
    return RATES.get(region.upper(), 0.0)


def with_tax(amount, region):
    return amount * (1 + rate_for(region))
