# Unstable Build LLC ("COMPANY") CONFIDENTIAL
#
# Unpublished Copyright (c) 2017-2026 Unstable Build, All Rights Reserved.
#
# NOTICE: All information contained herein is, and remains the property of COMPANY.
# The intellectual and technical concepts contained herein are proprietary to
# COMPANY and may be covered by U.S. and Foreign Patents, patents in process,
# and are protected by trade secret or copyright law. Dissemination of this information
# or reproduction of this material is strictly forbidden unless prior written permission
# is obtained from COMPANY. Access to the source code contained herein is hereby
# forbidden to anyone except current COMPANY employees, managers or contractors who
# have executed Confidentiality and Non-disclosure agreements explicitly covering such access.
#
# The copyright notice above does not evidence any actual or intended publication or
# disclosure of this source code, which includes information that is confidential and/or
# proprietary, and is a trade secret, of COMPANY.

"""Tiny program with a deliberate off-by-one bug used by the
debugpy e2e tests. sum_to iterates one past n, so sum_to(5)
returns 21 instead of 15. It is a clean breakpoint target:
stop inside sum_to, inspect i and total at the boundary.

other is a sibling helper that reuses the same local names
(n, total) so the tests can verify scope filtering. always_false
has a single literal-only statement (return False) used as a
breakpoint target that emits no identifier captures.

When invoked with the single argument "wait", main loops
calling sum_to forever (with a small sleep between iterations)
so the attach test has time to spawn the process, connect
debugpy to it, and reliably hit a breakpoint inside sum_to.
"""

import sys
import time


def sum_to(n):
    total = 0

    for i in range(1, n + 2):
        total += i
    return total


def other(n):
    total = n * 2
    return total


def always_false():
    return False


def main():
    if len(sys.argv) > 1 and sys.argv[1] == "wait":
        while True:
            sum_to(5)
            other(3)
            always_false()
            time.sleep(0.05)
    result = sum_to(5)
    print("Sum:", result)


if __name__ == "__main__":
    main()

# trailing comment: a breakpoint target past the last statement
