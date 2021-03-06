// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This file contains operations on unsigned multi-precision integers.
// These are the building blocks for the operations on signed integers
// and rationals.

// This package implements multi-precision arithmetic (big numbers).
// The following numeric types are supported:
//
//	- Int	signed integers
//
// All methods on Int take the result as the receiver; if it is one
// of the operands it may be overwritten (and its memory reused).
// To enable chaining of operations, the result is also returned.
//
// If possible, one should use big over bignum as the latter is headed for
// deprecation.
//
package big

import "rand"

// An unsigned integer x of the form
//
//   x = x[n-1]*_B^(n-1) + x[n-2]*_B^(n-2) + ... + x[1]*_B + x[0]
//
// with 0 <= x[i] < _B and 0 <= i < n is stored in a slice of length n,
// with the digits x[i] as the slice elements.
//
// A number is normalized if the slice contains no leading 0 digits.
// During arithmetic operations, denormalized values may occur but are
// always normalized before returning the final result. The normalized
// representation of 0 is the empty or nil slice (length = 0).

type nat []Word

var (
	natOne = nat{1}
	natTwo = nat{2}
)


func (z nat) clear() nat {
	for i := range z {
		z[i] = 0
	}
	return z
}


func (z nat) norm() nat {
	i := len(z)
	for i > 0 && z[i-1] == 0 {
		i--
	}
	z = z[0:i]
	return z
}


func (z nat) make(m int) nat {
	if cap(z) > m {
		return z[0:m] // reuse z - has at least one extra word for a carry, if any
	}

	c := 4 // minimum capacity
	if m > c {
		c = m
	}
	return make(nat, m, c+1) // +1: extra word for a carry, if any
}


func (z nat) new(x uint64) nat {
	if x == 0 {
		return z.make(0)
	}

	// single-digit values
	if x == uint64(Word(x)) {
		z = z.make(1)
		z[0] = Word(x)
		return z
	}

	// compute number of words n required to represent x
	n := 0
	for t := x; t > 0; t >>= _W {
		n++
	}

	// split x into n words
	z = z.make(n)
	for i := 0; i < n; i++ {
		z[i] = Word(x & _M)
		x >>= _W
	}

	return z
}


func (z nat) set(x nat) nat {
	z = z.make(len(x))
	for i, d := range x {
		z[i] = d
	}
	return z
}


func (z nat) add(x, y nat) nat {
	m := len(x)
	n := len(y)

	switch {
	case m < n:
		return z.add(y, x)
	case m == 0:
		// n == 0 because m >= n; result is 0
		return z.make(0)
	case n == 0:
		// result is x
		return z.set(x)
	}
	// m > 0

	z = z.make(m)
	c := addVV(&z[0], &x[0], &y[0], n)
	if m > n {
		c = addVW(&z[n], &x[n], c, m-n)
	}
	if c > 0 {
		z = z[0 : m+1]
		z[m] = c
	}

	return z
}


func (z nat) sub(x, y nat) nat {
	m := len(x)
	n := len(y)

	switch {
	case m < n:
		panic("underflow")
	case m == 0:
		// n == 0 because m >= n; result is 0
		return z.make(0)
	case n == 0:
		// result is x
		return z.set(x)
	}
	// m > 0

	z = z.make(m)
	c := subVV(&z[0], &x[0], &y[0], n)
	if m > n {
		c = subVW(&z[n], &x[n], c, m-n)
	}
	if c != 0 {
		panic("underflow")
	}

	return z.norm()
}


func (x nat) cmp(y nat) (r int) {
	m := len(x)
	n := len(y)
	if m != n || m == 0 {
		switch {
		case m < n:
			r = -1
		case m > n:
			r = 1
		}
		return
	}

	i := m - 1
	for i > 0 && x[i] == y[i] {
		i--
	}

	switch {
	case x[i] < y[i]:
		r = -1
	case x[i] > y[i]:
		r = 1
	}
	return
}


func (z nat) mulAddWW(x nat, y, r Word) nat {
	m := len(x)
	if m == 0 || y == 0 {
		return z.new(uint64(r)) // result is r
	}
	// m > 0

	z = z.make(m)
	c := mulAddVWW(&z[0], &x[0], y, r, m)
	if c > 0 {
		z = z[0 : m+1]
		z[m] = c
	}

	return z
}


// basicMul multiplies x and y and leaves the result in z.
// The (non-normalized) result is placed in z[0 : len(x) + len(y)].
func basicMul(z, x, y nat) {
	// initialize z
	for i := range z[0 : len(x)+len(y)] {
		z[i] = 0
	}
	// multiply
	for i, d := range y {
		if d != 0 {
			z[len(x)+i] = addMulVVW(&z[i], &x[0], d, len(x))
		}
	}
}


// Fast version of z[0:n+n>>1].add(z[0:n+n>>1], x[0:n]) w/o bounds checks.
// Factored out for readability - do not use outside karatsuba.
func karatsubaAdd(z, x nat, n int) {
	if c := addVV(&z[0], &z[0], &x[0], n); c != 0 {
		addVW(&z[n], &z[n], c, n>>1)
	}
}


// Like karatsubaAdd, but does subtract.
func karatsubaSub(z, x nat, n int) {
	if c := subVV(&z[0], &z[0], &x[0], n); c != 0 {
		subVW(&z[n], &z[n], c, n>>1)
	}
}


// Operands that are shorter than karatsubaThreshold are multiplied using
// "grade school" multiplication; for longer operands the Karatsuba algorithm
// is used.
var karatsubaThreshold int = 32 // computed by calibrate.go

// karatsuba multiplies x and y and leaves the result in z.
// Both x and y must have the same length n and n must be a
// power of 2. The result vector z must have len(z) >= 6*n.
// The (non-normalized) result is placed in z[0 : 2*n].
func karatsuba(z, x, y nat) {
	n := len(y)

	// Switch to basic multiplication if numbers are odd or small.
	// (n is always even if karatsubaThreshold is even, but be
	// conservative)
	if n&1 != 0 || n < karatsubaThreshold || n < 2 {
		basicMul(z, x, y)
		return
	}
	// n&1 == 0 && n >= karatsubaThreshold && n >= 2

	// Karatsuba multiplication is based on the observation that
	// for two numbers x and y with:
	//
	//   x = x1*b + x0
	//   y = y1*b + y0
	//
	// the product x*y can be obtained with 3 products z2, z1, z0
	// instead of 4:
	//
	//   x*y = x1*y1*b*b + (x1*y0 + x0*y1)*b + x0*y0
	//       =    z2*b*b +              z1*b +    z0
	//
	// with:
	//
	//   xd = x1 - x0
	//   yd = y0 - y1
	//
	//   z1 =      xd*yd                    + z1 + z0
	//      = (x1-x0)*(y0 - y1)             + z1 + z0
	//      = x1*y0 - x1*y1 - x0*y0 + x0*y1 + z1 + z0
	//      = x1*y0 -    z1 -    z0 + x0*y1 + z1 + z0
	//      = x1*y0                 + x0*y1

	// split x, y into "digits"
	n2 := n >> 1              // n2 >= 1
	x1, x0 := x[n2:], x[0:n2] // x = x1*b + y0
	y1, y0 := y[n2:], y[0:n2] // y = y1*b + y0

	// z is used for the result and temporary storage:
	//
	//   6*n     5*n     4*n     3*n     2*n     1*n     0*n
	// z = [z2 copy|z0 copy| xd*yd | yd:xd | x1*y1 | x0*y0 ]
	//
	// For each recursive call of karatsuba, an unused slice of
	// z is passed in that has (at least) half the length of the
	// caller's z.

	// compute z0 and z2 with the result "in place" in z
	karatsuba(z, x0, y0)     // z0 = x0*y0
	karatsuba(z[n:], x1, y1) // z2 = x1*y1

	// compute xd (or the negative value if underflow occurs)
	s := 1 // sign of product xd*yd
	xd := z[2*n : 2*n+n2]
	if subVV(&xd[0], &x1[0], &x0[0], n2) != 0 { // x1-x0
		s = -s
		subVV(&xd[0], &x0[0], &x1[0], n2) // x0-x1
	}

	// compute yd (or the negative value if underflow occurs)
	yd := z[2*n+n2 : 3*n]
	if subVV(&yd[0], &y0[0], &y1[0], n2) != 0 { // y0-y1
		s = -s
		subVV(&yd[0], &y1[0], &y0[0], n2) // y1-y0
	}

	// p = (x1-x0)*(y0-y1) == x1*y0 - x1*y1 - x0*y0 + x0*y1 for s > 0
	// p = (x0-x1)*(y0-y1) == x0*y0 - x0*y1 - x1*y0 + x1*y1 for s < 0
	p := z[n*3:]
	karatsuba(p, xd, yd)

	// save original z2:z0
	// (ok to use upper half of z since we're done recursing)
	r := z[n*4:]
	copy(r, z)

	// add up all partial products
	//
	//   2*n     n     0
	// z = [ z2  | z0  ]
	//   +    [ z0  ]
	//   +    [ z2  ]
	//   +    [  p  ]
	//
	karatsubaAdd(z[n2:], r, n)
	karatsubaAdd(z[n2:], r[n:], n)
	if s > 0 {
		karatsubaAdd(z[n2:], p, n)
	} else {
		karatsubaSub(z[n2:], p, n)
	}
}


// alias returns true if x and y share the same base array.
func alias(x, y nat) bool {
	return cap(x) > 0 && cap(y) > 0 && &x[0:cap(x)][cap(x)-1] == &y[0:cap(y)][cap(y)-1]
}


// addAt implements z += x*(1<<(_W*i)); z must be long enough.
// (we don't use nat.add because we need z to stay the same
// slice, and we don't need to normalize z after each addition)
func addAt(z, x nat, i int) {
	if n := len(x); n > 0 {
		if c := addVV(&z[i], &z[i], &x[0], n); c != 0 {
			j := i + n
			if j < len(z) {
				addVW(&z[j], &z[j], c, len(z)-j)
			}
		}
	}
}


func max(x, y int) int {
	if x > y {
		return x
	}
	return y
}


// karatsubaLen computes an approximation to the maximum k <= n such that
// k = p<<i for a number p <= karatsubaThreshold and an i >= 0. Thus, the
// result is the largest number that can be divided repeatedly by 2 before
// becoming about the value of karatsubaThreshold.
func karatsubaLen(n int) int {
	i := uint(0)
	for n > karatsubaThreshold {
		n >>= 1
		i++
	}
	return n << i
}


func (z nat) mul(x, y nat) nat {
	m := len(x)
	n := len(y)

	switch {
	case m < n:
		return z.mul(y, x)
	case m == 0 || n == 0:
		return z.make(0)
	case n == 1:
		return z.mulAddWW(x, y[0], 0)
	}
	// m >= n > 1

	// determine if z can be reused
	if alias(z, x) || alias(z, y) {
		z = nil // z is an alias for x or y - cannot reuse
	}

	// use basic multiplication if the numbers are small
	if n < karatsubaThreshold || n < 2 {
		z = z.make(m + n)
		basicMul(z, x, y)
		return z.norm()
	}
	// m >= n && n >= karatsubaThreshold && n >= 2

	// determine Karatsuba length k such that
	//
	//   x = x1*b + x0
	//   y = y1*b + y0  (and k <= len(y), which implies k <= len(x))
	//   b = 1<<(_W*k)  ("base" of digits xi, yi)
	//
	k := karatsubaLen(n)
	// k <= n

	// multiply x0 and y0 via Karatsuba
	x0 := x[0:k]              // x0 is not normalized
	y0 := y[0:k]              // y0 is not normalized
	z = z.make(max(6*k, m+n)) // enough space for karatsuba of x0*y0 and full result of x*y
	karatsuba(z, x0, y0)
	z = z[0 : m+n] // z has final length but may be incomplete, upper portion is garbage

	// If x1 and/or y1 are not 0, add missing terms to z explicitly:
	//
	//     m+n       2*k       0
	//   z = [   ...   | x0*y0 ]
	//     +   [ x1*y1 ]
	//     +   [ x1*y0 ]
	//     +   [ x0*y1 ]
	//
	if k < n || m != n {
		x1 := x[k:] // x1 is normalized because x is
		y1 := y[k:] // y1 is normalized because y is
		var t nat
		t = t.mul(x1, y1)
		copy(z[2*k:], t)
		z[2*k+len(t):].clear() // upper portion of z is garbage
		t = t.mul(x1, y0.norm())
		addAt(z, t, k)
		t = t.mul(x0.norm(), y1)
		addAt(z, t, k)
	}

	return z.norm()
}


// mulRange computes the product of all the unsigned integers in the
// range [a, b] inclusively. If a > b (empty range), the result is 1.
func (z nat) mulRange(a, b uint64) nat {
	switch {
	case a == 0:
		// cut long ranges short (optimization)
		return z.new(0)
	case a > b:
		return z.new(1)
	case a == b:
		return z.new(a)
	case a+1 == b:
		return z.mul(nat(nil).new(a), nat(nil).new(b))
	}
	m := (a + b) / 2
	return z.mul(nat(nil).mulRange(a, m), nat(nil).mulRange(m+1, b))
}


// q = (x-r)/y, with 0 <= r < y
func (z nat) divW(x nat, y Word) (q nat, r Word) {
	m := len(x)
	switch {
	case y == 0:
		panic("division by zero")
	case y == 1:
		q = z.set(x) // result is x
		return
	case m == 0:
		q = z.make(0) // result is 0
		return
	}
	// m > 0
	z = z.make(m)
	r = divWVW(&z[0], 0, &x[0], y, m)
	q = z.norm()
	return
}


func (z nat) div(z2, u, v nat) (q, r nat) {
	if len(v) == 0 {
		panic("division by zero")
	}

	if u.cmp(v) < 0 {
		q = z.make(0)
		r = z2.set(u)
		return
	}

	if len(v) == 1 {
		var rprime Word
		q, rprime = z.divW(u, v[0])
		if rprime > 0 {
			r = z2.make(1)
			r[0] = rprime
		} else {
			r = z2.make(0)
		}
		return
	}

	q, r = z.divLarge(z2, u, v)
	return
}


// q = (uIn-r)/v, with 0 <= r < y
// See Knuth, Volume 2, section 4.3.1, Algorithm D.
// Preconditions:
//    len(v) >= 2
//    len(uIn) >= len(v)
func (z nat) divLarge(z2, uIn, v nat) (q, r nat) {
	n := len(v)
	m := len(uIn) - len(v)

	var u nat
	if z2 == nil || &z2[0] == &uIn[0] {
		u = u.make(len(uIn) + 1).clear() // uIn is an alias for z2
	} else {
		u = z2.make(len(uIn) + 1).clear()
	}
	qhatv := make(nat, len(v)+1)
	q = z.make(m + 1)

	// D1.
	shift := leadingZeros(v[n-1])
	v.shiftLeftDeprecated(v, shift)
	u.shiftLeftDeprecated(uIn, shift)
	u[len(uIn)] = uIn[len(uIn)-1] >> (_W - shift)

	// D2.
	for j := m; j >= 0; j-- {
		// D3.
		var qhat Word
		if u[j+n] == v[n-1] {
			qhat = _B - 1
		} else {
			var rhat Word
			qhat, rhat = divWW_g(u[j+n], u[j+n-1], v[n-1])

			// x1 | x2 = q̂v_{n-2}
			x1, x2 := mulWW_g(qhat, v[n-2])
			// test if q̂v_{n-2} > br̂ + u_{j+n-2}
			for greaterThan(x1, x2, rhat, u[j+n-2]) {
				qhat--
				prevRhat := rhat
				rhat += v[n-1]
				// v[n-1] >= 0, so this tests for overflow.
				if rhat < prevRhat {
					break
				}
				x1, x2 = mulWW_g(qhat, v[n-2])
			}
		}

		// D4.
		qhatv[len(v)] = mulAddVWW(&qhatv[0], &v[0], qhat, 0, len(v))

		c := subVV(&u[j], &u[j], &qhatv[0], len(qhatv))
		if c != 0 {
			c := addVV(&u[j], &u[j], &v[0], len(v))
			u[j+len(v)] += c
			qhat--
		}

		q[j] = qhat
	}

	q = q.norm()
	u.shiftRightDeprecated(u, shift)
	v.shiftRightDeprecated(v, shift)
	r = u.norm()

	return q, r
}


// Length of x in bits. x must be normalized.
func (x nat) bitLen() int {
	if i := len(x) - 1; i >= 0 {
		return i*_W + bitLen(x[i])
	}
	return 0
}


func hexValue(ch byte) int {
	var d byte
	switch {
	case '0' <= ch && ch <= '9':
		d = ch - '0'
	case 'a' <= ch && ch <= 'f':
		d = ch - 'a' + 10
	case 'A' <= ch && ch <= 'F':
		d = ch - 'A' + 10
	default:
		return -1
	}
	return int(d)
}


// scan returns the natural number corresponding to the
// longest possible prefix of s representing a natural number in a
// given conversion base, the actual conversion base used, and the
// prefix length. The syntax of natural numbers follows the syntax
// of unsigned integer literals in Go.
//
// If the base argument is 0, the string prefix determines the actual
// conversion base. A prefix of ``0x'' or ``0X'' selects base 16; the
// ``0'' prefix selects base 8. Otherwise the selected base is 10.
//
func (z nat) scan(s string, base int) (nat, int, int) {
	// determine base if necessary
	i, n := 0, len(s)
	if base == 0 {
		base = 10
		if n > 0 && s[0] == '0' {
			if n > 1 && (s[1] == 'x' || s[1] == 'X') {
				if n == 2 {
					// Reject a string which is just '0x' as nonsense.
					return nil, 0, 0
				}
				base, i = 16, 2
			} else {
				base, i = 8, 1
			}
		}
	}
	if base < 2 || 16 < base {
		panic("illegal base")
	}

	// convert string
	z = z[0:0]
	for ; i < n; i++ {
		d := hexValue(s[i])
		if 0 <= d && d < base {
			z = z.mulAddWW(z, Word(base), Word(d))
		} else {
			break
		}
	}

	return z, base, i
}


// string converts x to a string for a given base, with 2 <= base <= 16.
// TODO(gri) in the style of the other routines, perhaps this should take
//           a []byte buffer and return it
func (x nat) string(base int) string {
	if base < 2 || 16 < base {
		panic("illegal base")
	}

	if len(x) == 0 {
		return "0"
	}

	// allocate buffer for conversion
	i := x.bitLen()/log2(Word(base)) + 1 // +1: round up
	s := make([]byte, i)

	// don't destroy x
	q := nat(nil).set(x)

	// convert
	for len(q) > 0 {
		i--
		var r Word
		q, r = q.divW(q, Word(base))
		s[i] = "0123456789abcdef"[r]
	}

	return string(s[i:])
}


const deBruijn32 = 0x077CB531

var deBruijn32Lookup = []byte{
	0, 1, 28, 2, 29, 14, 24, 3, 30, 22, 20, 15, 25, 17, 4, 8,
	31, 27, 13, 23, 21, 19, 16, 7, 26, 12, 18, 6, 11, 5, 10, 9,
}

const deBruijn64 = 0x03f79d71b4ca8b09

var deBruijn64Lookup = []byte{
	0, 1, 56, 2, 57, 49, 28, 3, 61, 58, 42, 50, 38, 29, 17, 4,
	62, 47, 59, 36, 45, 43, 51, 22, 53, 39, 33, 30, 24, 18, 12, 5,
	63, 55, 48, 27, 60, 41, 37, 16, 46, 35, 44, 21, 52, 32, 23, 11,
	54, 26, 40, 15, 34, 20, 31, 10, 25, 14, 19, 9, 13, 8, 7, 6,
}

// trailingZeroBits returns the number of consecutive zero bits on the right
// side of the given Word.
// See Knuth, volume 4, section 7.3.1
func trailingZeroBits(x Word) int {
	// x & -x leaves only the right-most bit set in the word. Let k be the
	// index of that bit. Since only a single bit is set, the value is two
	// to the power of k. Multipling by a power of two is equivalent to
	// left shifting, in this case by k bits.  The de Bruijn constant is
	// such that all six bit, consecutive substrings are distinct.
	// Therefore, if we have a left shifted version of this constant we can
	// find by how many bits it was shifted by looking at which six bit
	// substring ended up at the top of the word.
	switch _W {
	case 32:
		return int(deBruijn32Lookup[((x&-x)*deBruijn32)>>27])
	case 64:
		return int(deBruijn64Lookup[((x&-x)*(deBruijn64&_M))>>58])
	default:
		panic("Unknown word size")
	}

	return 0
}


// z = x << s
func (z nat) shl(x nat, s uint) nat {
	m := len(x)
	if m == 0 {
		return z.make(0)
	}
	// m > 0

	// determine if z can be reused
	// TODO(gri) change shlVW so we don't need this
	if alias(z, x) {
		z = nil // z is an alias for x - cannot reuse
	}

	n := m + int(s/_W)
	z = z.make(n + 1)
	z[n] = shlVW(&z[n-m], &x[0], Word(s%_W), m)

	return z.norm()
}


// z = x >> s
func (z nat) shr(x nat, s uint) nat {
	m := len(x)
	n := m - int(s/_W)
	if n <= 0 {
		return z.make(0)
	}
	// n > 0

	// determine if z can be reused
	// TODO(gri) change shrVW so we don't need this
	if alias(z, x) {
		z = nil // z is an alias for x - cannot reuse
	}

	z = z.make(n)
	shrVW(&z[0], &x[m-n], Word(s%_W), n)

	return z.norm()
}


// TODO(gri) Remove these shift functions once shlVW and shrVW can be
//           used directly in divLarge and powersOfTwoDecompose
//
// To avoid losing the top n bits, z should be sized so that
// len(z) == len(x) + 1.
func (z nat) shiftLeftDeprecated(x nat, n uint) nat {
	if len(x) == 0 {
		return x
	}

	ñ := _W - n
	m := x[len(x)-1]
	if len(z) > len(x) {
		z[len(x)] = m >> ñ
	}
	for i := len(x) - 1; i >= 1; i-- {
		y := x[i-1]
		z[i] = m<<n | y>>ñ
		m = y
	}
	z[0] = m << n
	return z
}


func (z nat) shiftRightDeprecated(x nat, n uint) nat {
	if len(x) == 0 {
		return x
	}

	ñ := _W - n
	m := x[0]
	for i := 0; i < len(x)-1; i++ {
		y := x[i+1]
		z[i] = m>>n | y<<ñ
		m = y
	}
	z[len(x)-1] = m >> n
	return z
}


func (z nat) and(x, y nat) nat {
	m := len(x)
	n := len(y)
	if m > n {
		m = n
	}
	// m <= n

	z = z.make(m)
	for i := 0; i < m; i++ {
		z[i] = x[i] & y[i]
	}

	return z.norm()
}


func (z nat) andNot(x, y nat) nat {
	m := len(x)
	n := len(y)
	if n > m {
		n = m
	}
	// m >= n

	z = z.make(m)
	for i := 0; i < n; i++ {
		z[i] = x[i] &^ y[i]
	}
	copy(z[n:m], x[n:m])

	return z.norm()
}


func (z nat) or(x, y nat) nat {
	m := len(x)
	n := len(y)
	s := x
	if m < n {
		n, m = m, n
		s = y
	}
	// n >= m

	z = z.make(n)
	for i := 0; i < m; i++ {
		z[i] = x[i] | y[i]
	}
	copy(z[m:n], s[m:n])

	return z.norm()
}


func (z nat) xor(x, y nat) nat {
	m := len(x)
	n := len(y)
	s := x
	if n < m {
		n, m = m, n
		s = y
	}
	// n >= m

	z = z.make(n)
	for i := 0; i < m; i++ {
		z[i] = x[i] ^ y[i]
	}
	copy(z[m:n], s[m:n])

	return z.norm()
}


// greaterThan returns true iff (x1<<_W + x2) > (y1<<_W + y2)
func greaterThan(x1, x2, y1, y2 Word) bool { return x1 > y1 || x1 == y1 && x2 > y2 }


// modW returns x % d.
func (x nat) modW(d Word) (r Word) {
	// TODO(agl): we don't actually need to store the q value.
	var q nat
	q = q.make(len(x))
	return divWVW(&q[0], 0, &x[0], d, len(x))
}


// powersOfTwoDecompose finds q and k such that q * 1<<k = n and q is odd.
func (n nat) powersOfTwoDecompose() (q nat, k Word) {
	if len(n) == 0 {
		return n, 0
	}

	zeroWords := 0
	for n[zeroWords] == 0 {
		zeroWords++
	}
	// One of the words must be non-zero by invariant, therefore
	// zeroWords < len(n).
	x := trailingZeroBits(n[zeroWords])

	q = q.make(len(n) - zeroWords)
	q.shiftRightDeprecated(n[zeroWords:], uint(x))
	q = q.norm()

	k = Word(_W*zeroWords + x)
	return
}


// random creates a random integer in [0..limit), using the space in z if
// possible. n is the bit length of limit.
func (z nat) random(rand *rand.Rand, limit nat, n int) nat {
	bitLengthOfMSW := uint(n % _W)
	if bitLengthOfMSW == 0 {
		bitLengthOfMSW = _W
	}
	mask := Word((1 << bitLengthOfMSW) - 1)
	z = z.make(len(limit))

	for {
		for i := range z {
			switch _W {
			case 32:
				z[i] = Word(rand.Uint32())
			case 64:
				z[i] = Word(rand.Uint32()) | Word(rand.Uint32())<<32
			}
		}

		z[len(limit)-1] &= mask

		if z.cmp(limit) < 0 {
			break
		}
	}

	return z.norm()
}


// If m != nil, expNN calculates x**y mod m. Otherwise it calculates x**y. It
// reuses the storage of z if possible.
func (z nat) expNN(x, y, m nat) nat {
	if len(y) == 0 {
		z = z.make(1)
		z[0] = 1
		return z
	}

	if m != nil {
		// We likely end up being as long as the modulus.
		z = z.make(len(m))
	}
	z = z.set(x)
	v := y[len(y)-1]
	// It's invalid for the most significant word to be zero, therefore we
	// will find a one bit.
	shift := leadingZeros(v) + 1
	v <<= shift
	var q nat

	const mask = 1 << (_W - 1)

	// We walk through the bits of the exponent one by one. Each time we
	// see a bit, we square, thus doubling the power. If the bit is a one,
	// we also multiply by x, thus adding one to the power.

	w := _W - int(shift)
	for j := 0; j < w; j++ {
		z = z.mul(z, z)

		if v&mask != 0 {
			z = z.mul(z, x)
		}

		if m != nil {
			q, z = q.div(z, z, m)
		}

		v <<= 1
	}

	for i := len(y) - 2; i >= 0; i-- {
		v = y[i]

		for j := 0; j < _W; j++ {
			z = z.mul(z, z)

			if v&mask != 0 {
				z = z.mul(z, x)
			}

			if m != nil {
				q, z = q.div(z, z, m)
			}

			v <<= 1
		}
	}

	return z
}


// probablyPrime performs reps Miller-Rabin tests to check whether n is prime.
// If it returns true, n is prime with probability 1 - 1/4^reps.
// If it returns false, n is not prime.
func (n nat) probablyPrime(reps int) bool {
	if len(n) == 0 {
		return false
	}

	if len(n) == 1 {
		if n[0] < 2 {
			return false
		}

		if n[0]%2 == 0 {
			return n[0] == 2
		}

		// We have to exclude these cases because we reject all
		// multiples of these numbers below.
		switch n[0] {
		case 3, 5, 7, 11, 13, 17, 19, 23, 29, 31, 37, 41, 43, 47, 53:
			return true
		}
	}

	const primesProduct32 = 0xC0CFD797         // Π {p ∈ primes, 2 < p <= 29}
	const primesProduct64 = 0xE221F97C30E94E1D // Π {p ∈ primes, 2 < p <= 53}

	var r Word
	switch _W {
	case 32:
		r = n.modW(primesProduct32)
	case 64:
		r = n.modW(primesProduct64 & _M)
	default:
		panic("Unknown word size")
	}

	if r%3 == 0 || r%5 == 0 || r%7 == 0 || r%11 == 0 ||
		r%13 == 0 || r%17 == 0 || r%19 == 0 || r%23 == 0 || r%29 == 0 {
		return false
	}

	if _W == 64 && (r%31 == 0 || r%37 == 0 || r%41 == 0 ||
		r%43 == 0 || r%47 == 0 || r%53 == 0) {
		return false
	}

	nm1 := nat(nil).sub(n, natOne)
	// 1<<k * q = nm1;
	q, k := nm1.powersOfTwoDecompose()

	nm3 := nat(nil).sub(nm1, natTwo)
	rand := rand.New(rand.NewSource(int64(n[0])))

	var x, y, quotient nat
	nm3Len := nm3.bitLen()

NextRandom:
	for i := 0; i < reps; i++ {
		x = x.random(rand, nm3, nm3Len)
		x = x.add(x, natTwo)
		y = y.expNN(x, q, n)
		if y.cmp(natOne) == 0 || y.cmp(nm1) == 0 {
			continue
		}
		for j := Word(1); j < k; j++ {
			y = y.mul(y, y)
			quotient, y = quotient.div(y, y, n)
			if y.cmp(nm1) == 0 {
				continue NextRandom
			}
			if y.cmp(natOne) == 0 {
				return false
			}
		}
		return false
	}

	return true
}
