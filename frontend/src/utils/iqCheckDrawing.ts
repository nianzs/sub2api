import type { IQCheckDrawing } from '@/types'

/**
 * Data URL for showing an IQ check SVG through <img>. Never insert the SVG as
 * HTML: an image runs no script and loads no external resource.
 */
export function iqCheckDrawingSrc(svg: string): string {
  return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`
}

export function iqCheckDrawingSeconds(drawing: Pick<IQCheckDrawing, 'latency_ms'>): string {
  return (Math.max(drawing.latency_ms, 0) / 1000).toFixed(1)
}
