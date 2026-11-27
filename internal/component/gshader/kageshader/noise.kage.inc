// Appended to the programs that use noise, as Kage has no imports.

// hash returns a value in [0, 1) that looks random but is always the same
// for the same p.
func hash(p vec3) float {
	p = fract(p * 0.1031)
	p += dot(p, p.zyx+31.32)
	return fract((p.x + p.y) * p.z)
}

// noise is value noise in [0, 1) at p that repeats every period along z,
// which must be whole, and differs for each seed.
func noise(p vec3, period float, seed float) float {
	i := floor(p)
	f := fract(p)
	u := f * f * (3 - 2*f)
	z0 := mod(i.z, period) + seed
	z1 := mod(i.z+1, period) + seed
	near := mix(
		mix(hash(vec3(i.x, i.y, z0)), hash(vec3(i.x+1, i.y, z0)), u.x),
		mix(hash(vec3(i.x, i.y+1, z0)), hash(vec3(i.x+1, i.y+1, z0)), u.x),
		u.y)
	far := mix(
		mix(hash(vec3(i.x, i.y, z1)), hash(vec3(i.x+1, i.y, z1)), u.x),
		mix(hash(vec3(i.x, i.y+1, z1)), hash(vec3(i.x+1, i.y+1, z1)), u.x),
		u.y)
	return mix(near, far, u.z)
}

// fbm sums octaves, at most 5, of noise at p, each lacunarity times finer
// and gain times fainter than the last, the first at full strength. The
// noise changes as progress goes from 0 to 1, by steps lattice steps for
// the first octave, and ends where it started.
func fbm(p vec2, progress float, steps float, seed float, octaves int, lacunarity float, gain float) float {
	total := 0.0
	amplitude := 1.0
	scale := 1.0
	for i := 0; i < 5; i++ {
		if i >= octaves {
			break
		}
		// Each octave moves a whole number of its own steps, so that each
		// one ends where it started.
		period := max(floor(steps*scale+0.5), 1)
		z := period * progress
		if steps <= 0 {
			z = 0
		}
		total += amplitude * noise(vec3(p, z), period, seed)
		p *= lacunarity
		scale *= lacunarity
		amplitude *= gain
	}
	return total
}

// seedOffset folds seed, which may be any whole number, into an offset the
// hash tells apart.
func seedOffset(seed float) float {
	return mod(seed, 997) * 1.618
}

// rgb2hsv and hsv2rgb convert between RGB and hue, saturation and value,
// each in [0, 1].
func rgb2hsv(c vec3) vec3 {
	k := vec4(0, -1.0/3.0, 2.0/3.0, -1)
	p := mix(vec4(c.b, c.g, k.w, k.z), vec4(c.g, c.b, k.x, k.y), step(c.b, c.g))
	q := mix(vec4(p.x, p.y, p.w, c.r), vec4(c.r, p.y, p.z, p.x), step(p.x, c.r))
	d := q.x - min(q.w, q.y)
	e := 1.0e-10
	return vec3(abs(q.z+(q.w-q.y)/(6*d+e)), d/(q.x+e), q.x)
}

func hsv2rgb(c vec3) vec3 {
	k := vec4(1, 2.0/3.0, 1.0/3.0, 3)
	p := abs(fract(c.xxx+k.xyz)*6 - k.www)
	return c.z * mix(k.xxx, clamp(p-k.xxx, 0, 1), c.y)
}
