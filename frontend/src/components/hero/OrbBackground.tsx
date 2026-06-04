/**
 * Overlapping blurred div layers that consume CSS custom properties
 * (--orb-x, --orb-y, --orb-scale, --orb-intensity) set by useOrbAnimation.
 *
 * Layers:
 * 1. Primary orb — orange radial gradient, follows --orb-x/y
 * 2. Secondary bloom — amber, moves inversely for depth
 * 3. Noise grain overlay — SVG feTurbulence at low opacity for texture
 */
export function OrbBackground() {
  return (
    <div className="absolute inset-0 overflow-hidden" aria-hidden="true">
      {/* Primary orb */}
      <div
        className="absolute w-[200px] h-[200px] md:w-[300px] md:h-[300px] lg:w-[400px] lg:h-[400px] rounded-full blur-[40px] md:blur-[60px] lg:blur-[80px]"
        style={{
          left: 'var(--orb-x, 50%)',
          top: 'var(--orb-y, 50%)',
          transform: 'translate(-50%, -50%) scale(var(--orb-scale, 1))',
          background: 'radial-gradient(circle, #fb923c 0%, #f97316 40%, transparent 70%)',
          opacity: 'var(--orb-intensity, 0.6)',
        }}
      />

      {/* Secondary bloom — moves inversely for depth */}
      <div
        className="absolute w-[140px] h-[140px] md:w-[200px] md:h-[200px] lg:w-[280px] lg:h-[280px] rounded-full blur-[30px] md:blur-[40px] lg:blur-[60px]"
        style={{
          left: 'calc(100% - var(--orb-x, 50%))',
          top: 'calc(100% - var(--orb-y, 50%))',
          transform: 'translate(-50%, -50%)',
          background: 'radial-gradient(circle, #fbbf24 0%, #f59e0b 50%, transparent 70%)',
          opacity: 'calc(var(--orb-intensity, 0.6) * 0.4)',
        }}
      />

      {/* Noise grain overlay */}
      <svg className="absolute inset-0 w-full h-full opacity-[0.03]" xmlns="http://www.w3.org/2000/svg">
        <filter id="hero-noise">
          <feTurbulence type="fractalNoise" baseFrequency="0.65" numOctaves="3" stitchTiles="stitch" />
        </filter>
        <rect width="100%" height="100%" filter="url(#hero-noise)" />
      </svg>
    </div>
  );
}
