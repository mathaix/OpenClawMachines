import { useEffect, useRef, type RefObject } from 'react';

/**
 * Drives organic orb motion via requestAnimationFrame, updating CSS custom
 * properties (--orb-x, --orb-y, --orb-scale, --orb-intensity) on the
 * container ref each frame. The orb pops in then drifts organically.
 * Respects prefers-reduced-motion.
 */
export function useOrbAnimation(
  containerRef: RefObject<HTMLElement | null>,
  targetRef: RefObject<HTMLElement | null>,
) {
  const rafRef = useRef<number>(0);

  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;

    const mq = window.matchMedia('(prefers-reduced-motion: reduce)');
    if (mq.matches) {
      el.style.setProperty('--orb-x', '50%');
      el.style.setProperty('--orb-y', '50%');
      el.style.setProperty('--orb-scale', '1');
      el.style.setProperty('--orb-intensity', '0.6');
      return;
    }

    function getTargetPos(): { x: number; y: number } {
      const target = targetRef.current;
      const container = containerRef.current;
      if (!target || !container) return { x: 25, y: 35 };

      const tRect = target.getBoundingClientRect();
      const cRect = container.getBoundingClientRect();

      const x = ((tRect.left + tRect.width / 2 - cRect.left) / cRect.width) * 100;
      const y = ((tRect.top + tRect.height / 2 - cRect.top) / cRect.height) * 100;
      return { x, y };
    }

    const pop = getTargetPos();
    const start = performance.now();

    function animate(now: number) {
      const t = (now - start) / 1000;

      let x: number, y: number, scale: number, intensity: number;

      if (t < 0.6) {
        const p = t / 0.6;
        const eased = p * p;
        x = pop.x;
        y = pop.y;
        scale = 0.05 + eased * 0.75;
        intensity = eased * 0.9;
      } else if (t < 1.2) {
        const p = (t - 0.6) / 0.6;
        const eased = p * (2 - p);
        x = pop.x;
        y = pop.y;
        scale = 0.8 + 0.2 * eased;
        intensity = 0.9 - 0.15 * eased;
      } else {
        const driftT = t - 1.2;
        const ease = Math.min(1, driftT / 3);
        const eased = ease * ease * (3 - 2 * ease);

        const driftX = 50 + 18 * Math.sin(t * 0.3) + 8 * Math.sin(t * 0.7);
        const driftY = 50 + 12 * Math.sin(t * 0.4) + 6 * Math.sin(t * 0.9);

        x = pop.x + (driftX - pop.x) * eased;
        y = pop.y + (driftY - pop.y) * eased;
        scale = 1.0 + 0.1 * Math.sin(t * 0.5);
        intensity = 0.75 - 0.1 * eased + 0.15 * Math.sin(t * 0.35);
      }

      el!.style.setProperty('--orb-x', `${x}%`);
      el!.style.setProperty('--orb-y', `${y}%`);
      el!.style.setProperty('--orb-scale', `${scale}`);
      el!.style.setProperty('--orb-intensity', `${intensity}`);

      rafRef.current = requestAnimationFrame(animate);
    }

    rafRef.current = requestAnimationFrame(animate);

    return () => {
      cancelAnimationFrame(rafRef.current);
    };
  }, [containerRef, targetRef]);
}
