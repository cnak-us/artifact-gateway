// Thin React wrapper around uPlot. Mounts a uPlot instance into a host div,
// keeps the chart sized to the container via ResizeObserver, and re-renders
// in place when `data` or `series` change.
//
// data shape: [xValues, ...yValuesPerSeries] — uPlot's native AlignedData.
// series shape: array of uPlot series objects describing label/stroke/etc.,
//   prepended by a single x-axis spec ({}) inside this component.
import { useEffect, useRef } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';

export default function UPlotChart({ data, series, height = 200, yTickFormatter }) {
  const hostRef = useRef(null);
  const plotRef = useRef(null);
  const optsRef = useRef(null);

  // Build options once per series-shape change. Avoiding object recreation on
  // every data tick keeps uPlot from resetting cursor/zoom state.
  useEffect(() => {
    if (!hostRef.current) return;

    const styles = getComputedStyle(document.documentElement);
    const grid = `rgb(${styles.getPropertyValue('--g-text-primary').trim() || '36 41 46'} / 0.08)`;
    const axisText = styles.getPropertyValue('--g-text-secondary').trim() || 'rgba(36,41,46,0.7)';

    const opts = {
      width: hostRef.current.clientWidth || 600,
      height,
      cursor: { drag: { x: true, y: false }, focus: { prox: 24 } },
      legend: { show: true, live: true },
      scales: { x: { time: true }, y: { auto: true } },
      axes: [
        {
          stroke: axisText,
          grid: { stroke: grid, width: 1 },
          ticks: { stroke: grid, width: 1 },
        },
        {
          stroke: axisText,
          grid: { stroke: grid, width: 1 },
          ticks: { stroke: grid, width: 1 },
          values: yTickFormatter
            ? (u, splits) => splits.map(yTickFormatter)
            : undefined,
          size: 60,
        },
      ],
      series: [
        { label: 'time' },
        ...series,
      ],
    };
    optsRef.current = opts;

    plotRef.current?.destroy();
    plotRef.current = new uPlot(opts, data, hostRef.current);

    const ro = new ResizeObserver(() => {
      if (!plotRef.current || !hostRef.current) return;
      plotRef.current.setSize({ width: hostRef.current.clientWidth, height });
    });
    ro.observe(hostRef.current);

    return () => {
      ro.disconnect();
      plotRef.current?.destroy();
      plotRef.current = null;
    };
    // We intentionally rebuild when the series array identity changes (i.e.
    // when label set changes), not on every data tick — see setData below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [series, height, yTickFormatter]);

  // Cheap path for the common case: only the data array changed.
  useEffect(() => {
    if (plotRef.current && data) {
      plotRef.current.setData(data);
    }
  }, [data]);

  return <div ref={hostRef} className="w-full" style={{ minHeight: height }} />;
}
