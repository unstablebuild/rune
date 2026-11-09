# Design notes

Requests to upstream services go through the retry package, which waits
longer after every failure so a struggling dependency can recover.

Prices are stored in cents; totals are rounded once, at the end.
