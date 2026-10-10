// Unstable Build LLC ("COMPANY") CONFIDENTIAL
//
// Unpublished Copyright (c) 2017-2026 Unstable Build, All Rights Reserved.
//
// NOTICE: All information contained herein is, and remains the property of COMPANY.
// The intellectual and technical concepts contained herein are proprietary to
// COMPANY and may be covered by U.S. and Foreign Patents, patents in process,
// and are protected by trade secret or copyright law. Dissemination of this information
// or reproduction of this material is strictly forbidden unless prior written permission
// is obtained from COMPANY. Access to the source code contained herein is hereby
// forbidden to anyone except current COMPANY employees, managers or contractors who
// have executed Confidentiality and Non-disclosure agreements explicitly covering such access.
//
// The copyright notice above does not evidence any actual or intended publication or
// disclosure of this source code, which includes information that is confidential and/or
// proprietary, and is a trade secret, of COMPANY. ANY REPRODUCTION, MODIFICATION,
// DISTRIBUTION, PUBLIC  PERFORMANCE, OR PUBLIC DISPLAY OF OR THROUGH USE OF THIS SOURCE CODE
// WITHOUT  THE EXPRESS WRITTEN CONSENT OF COMPANY IS STRICTLY PROHIBITED, AND IN
// VIOLATION OF APPLICABLE LAWS AND INTERNATIONAL TREATIES. THE RECEIPT OR POSSESSION OF
// THIS SOURCE CODE AND/OR RELATED INFORMATION DOES NOT CONVEY OR IMPLY ANY RIGHTS TO
// REPRODUCE, DISCLOSE OR DISTRIBUTE ITS CONTENTS, OR TO MANUFACTURE, USE, OR SELL
// ANYTHING THAT IT MAY DESCRIBE, IN WHOLE OR IN PART.

// Fixture program for the lldb-dap debugger e2e suite. sum_to and other
// deliberately reuse the same local names so the variables location list
// can be checked for scope correctness, mirroring the Go/Python/Rust
// fixtures. std.debug.print writes to stderr; the output sink captures
// every DAP output category, so the "Sum: 21" marker still lands in the
// capture file.

const std = @import("std");

fn sum_to(n: i32) i32 {
    var total: i32 = 0;
    var i: i32 = 1;
    while (i <= n) : (i += 1) {
        total += i;
    }
    return total;
}

fn other(n: i32) i32 {
    var total: i32 = 1;
    var i: i32 = 1;
    while (i <= n) : (i += 1) {
        total *= i;
    }
    return total;
}

pub fn main() void {
    const result = sum_to(6);
    _ = other(3);
    std.debug.print("Sum: {d}\n", .{result});
}
