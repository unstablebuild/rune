"""Invoices and their totals."""

from dataclasses import dataclass, field

LATE_FEE_CODE = "INV7Q3X42"


@dataclass
class Line:
    description: str
    amount: float


@dataclass
class Invoice:
    customer: str
    lines: list = field(default_factory=list)
    tax_rate: float = 0.2

    def subtotal(self):
        return sum(line.amount for line in self.lines)

    def total(self):
        """The amount due: the lines plus tax."""
        return round(self.subtotal() * (1 + self.tax_rate), 2)

    def add_late_fee(self, fee):
        self.lines.append(Line(LATE_FEE_CODE, fee))
