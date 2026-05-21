// Admin monitoring page. Polls the in-process metrics collector and renders
// one uPlot chart per metric. Counters are converted to per-second rates;
// histograms show p50/p95/p99 latencies. The /metrics endpoint on the
// management port is unaffected — this is a UI on top of the same data.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { MdRefresh, MdShowChart, MdPauseCircle, MdPlayCircle } from 'react-icons/md';
import { admin } from '../../api/client.js';
import Button from '../../components/Button.jsx';
import Card from '../../components/Card.jsx';
import Spinner from '../../components/Spinner.jsx';
import ErrorBanner from '../../components/ErrorBanner.jsx';
import EmptyState from '../../components/EmptyState.jsx';
import Select from '../../components/Select.jsx';
import UPlotChart from '../../components/UPlotChart.jsx';

const RANGE_OPTIONS = [
  { value: '300',  label: 'Last 5m'  },
  { value: '900',  label: 'Last 15m' },
  { value: '1800', label: 'Last 30m' },
  { value: '3600', label: 'Last 1h'  },
];

const REFRESH_MS = 5000;

// Distinct strokes that are legible in light, dark, and low-light themes.
// uPlot draws series in the order we declare them; we cycle this palette.
const PALETTE = [
  '#3871dc', '#1b855e', '#e0226e', '#a352cc',
  '#ff9900', '#1f78c1', '#cc9d00', '#8f3bb8',
];

export default function Monitoring() {
  const [catalog, setCatalog] = useState(null);
  const [err, setErr] = useState(null);
  const [rangeSecs, setRangeSecs] = useState('900');
  const [paused, setPaused] = useState(false);
  const [seriesByName, setSeriesByName] = useState({}); // name → SeriesResponse

  const loadCatalog = useCallback(async () => {
    try {
      const res = await admin.metricsCatalog();
      setCatalog(res?.metrics || []);
    } catch (e) {
      setErr(e);
    }
  }, []);

  const loadAllSeries = useCallback(async (names) => {
    if (!names || names.length === 0) return;
    const results = await Promise.allSettled(
      names.map((n) => admin.metricsSeries(n, { sinceSecs: Number(rangeSecs) })),
    );
    const next = {};
    results.forEach((r, i) => {
      if (r.status === 'fulfilled') next[names[i]] = r.value;
    });
    setSeriesByName(next);
  }, [rangeSecs]);

  useEffect(() => { loadCatalog(); }, [loadCatalog]);

  // Drive a polling interval keyed on catalog + paused + range. We also
  // re-poll the catalog itself periodically so a metric registered after page
  // load (e.g. one whose first observation came from incoming traffic)
  // eventually shows up without a manual refresh.
  const namesRef = useRef([]);
  namesRef.current = useMemo(
    () => (catalog || []).map((m) => m.name),
    [catalog],
  );

  useEffect(() => {
    loadAllSeries(namesRef.current);
    if (paused) return undefined;
    const id = setInterval(() => {
      loadCatalog();
      loadAllSeries(namesRef.current);
    }, REFRESH_MS);
    return () => clearInterval(id);
  }, [catalog, rangeSecs, paused, loadAllSeries, loadCatalog]);

  const refresh = () => { loadCatalog(); loadAllSeries(namesRef.current); };

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-3 flex-wrap">
        <div>
          <h1 className="text-xl font-semibold">Monitoring</h1>
          <p className="text-sm text-g-text-secondary max-w-2xl">
            Live in-process metrics from the last hour. Scrape <code className="text-xs">/metrics</code> for long-term storage.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Select
            className="w-40"
            value={rangeSecs}
            onChange={(e) => setRangeSecs(e.target.value)}
            options={RANGE_OPTIONS}
          />
          <Button
            variant="outline"
            icon={paused ? <MdPlayCircle /> : <MdPauseCircle />}
            onClick={() => setPaused((p) => !p)}
          >
            {paused ? 'Resume' : 'Pause'}
          </Button>
          <Button variant="outline" icon={<MdRefresh />} onClick={refresh}>Refresh</Button>
        </div>
      </div>

      <ErrorBanner error={err} />

      {catalog === null ? (
        <Spinner label="Loading metrics" />
      ) : catalog.length === 0 ? (
        <EmptyState
          icon={MdShowChart}
          title="No metrics yet"
          description="The collector hasn't recorded any samples yet. Try refreshing in a few seconds."
        />
      ) : (
        <div className="grid grid-cols-1 xl:grid-cols-2 gap-4">
          {catalog.map((m) => (
            <MetricCard
              key={m.name}
              meta={m}
              data={seriesByName[m.name]}
              rangeSecs={Number(rangeSecs)}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function MetricCard({ meta, data, rangeSecs }) {
  const { aligned, series, ylabel } = useMemo(
    () => buildChartData(meta, data),
    [meta, data],
  );

  const yTickFormatter = useMemo(() => {
    if (meta.kind === 'histogram') return (v) => formatDuration(v);
    if (meta.kind === 'counter')   return (v) => formatRate(v);
    return (v) => formatNumber(v);
  }, [meta.kind]);

  const hasData = aligned && aligned[0] && aligned[0].length > 0;

  return (
    <Card>
      <div className="flex items-start justify-between gap-2 mb-2">
        <div className="min-w-0">
          <div className="font-mono text-sm truncate" title={meta.name}>{meta.name}</div>
          <div className="text-xs text-g-text-secondary">{meta.help || ' '}</div>
        </div>
        <div className="text-[10px] uppercase tracking-wider text-g-text-disabled shrink-0">
          {meta.kind}{ylabel ? ` · ${ylabel}` : ''}
        </div>
      </div>
      {hasData ? (
        <UPlotChart
          data={aligned}
          series={series}
          height={200}
          yTickFormatter={yTickFormatter}
        />
      ) : (
        <div className="h-[200px] flex items-center justify-center text-xs text-g-text-disabled">
          waiting for samples ({rangeSecs}s window)
        </div>
      )}
    </Card>
  );
}

// --- shaping helpers --------------------------------------------------------

// buildChartData turns a SeriesResponse from the server into the AlignedData
// + series-spec pair uPlot expects.
//
// Decisions:
//   - Counters → per-second rate (delta / dt). Cumulative counters are nearly
//     unreadable; rate is the natural unit. Negative values (process restart)
//     are clamped to 0.
//   - Histograms → keep only the p* sub-series; count/sum aren't useful here.
//   - Gauges → raw values.
//   - Summaries → raw quantile values.
function buildChartData(meta, resp) {
  if (!resp || !resp.series || resp.series.length === 0) {
    return { aligned: [[]], series: [], ylabel: '' };
  }

  // Collect the union of timestamps across all series for this metric.
  const tsSet = new Set();
  resp.series.forEach((s) => (s.points || []).forEach((p) => tsSet.add(p.ts)));
  const xs = Array.from(tsSet).sort((a, b) => a - b);

  let ylabel = '';
  let filtered = resp.series;
  if (meta.kind === 'histogram') {
    filtered = resp.series.filter((s) => s.sub && s.sub.startsWith('p'));
    ylabel = 'seconds';
  } else if (meta.kind === 'counter') {
    ylabel = '/sec';
  } else if (meta.kind === 'summary') {
    filtered = resp.series.filter((s) => s.sub && s.sub.startsWith('p'));
  }

  const yArrays = filtered.map((s) => {
    const byTs = new Map((s.points || []).map((p) => [p.ts, p.value]));
    const raw = xs.map((t) => (byTs.has(t) ? byTs.get(t) : null));
    if (meta.kind !== 'counter') return raw;

    // Per-second rate: y[i] = (raw[i] - raw[i-1]) / dt[i]
    const out = new Array(raw.length).fill(null);
    let prevV = null;
    let prevT = null;
    for (let i = 0; i < raw.length; i++) {
      const v = raw[i];
      const t = xs[i];
      if (v == null) { prevV = null; prevT = null; continue; }
      if (prevV != null && prevT != null) {
        const dt = t - prevT;
        const dv = v - prevV;
        out[i] = dt > 0 && dv >= 0 ? dv / dt : 0;
      }
      prevV = v;
      prevT = t;
    }
    return out;
  });

  const aligned = [xs, ...yArrays];
  const series = filtered.map((s, i) => ({
    label: labelFor(s, meta),
    stroke: PALETTE[i % PALETTE.length],
    width: 1.5,
    spanGaps: false,
    points: { show: false },
    value: (u, v) => (v == null ? '—' : formatValue(meta.kind, v)),
  }));

  return { aligned, series, ylabel };
}

function labelFor(s, meta) {
  const labelKeys = Object.keys(s.labels || {});
  const labels = labelKeys.length
    ? labelKeys.sort().map((k) => `${k}=${s.labels[k]}`).join(',')
    : '';
  const parts = [];
  if (s.sub) parts.push(s.sub);
  if (labels) parts.push(`{${labels}}`);
  if (parts.length === 0) return meta.name;
  return parts.join(' ');
}

// --- value formatting -------------------------------------------------------

function formatValue(kind, v) {
  if (kind === 'histogram' || kind === 'summary') return formatDuration(v);
  if (kind === 'counter') return formatRate(v);
  return formatNumber(v);
}

function formatNumber(v) {
  if (!Number.isFinite(v)) return '—';
  const a = Math.abs(v);
  if (a >= 1e9) return (v / 1e9).toFixed(2) + 'B';
  if (a >= 1e6) return (v / 1e6).toFixed(2) + 'M';
  if (a >= 1e3) return (v / 1e3).toFixed(2) + 'k';
  if (a >= 1)   return v.toFixed(2);
  if (a === 0)  return '0';
  return v.toPrecision(2);
}

function formatRate(v) {
  return formatNumber(v) + '/s';
}

function formatDuration(v) {
  if (!Number.isFinite(v)) return '—';
  if (v >= 1)      return v.toFixed(2) + 's';
  if (v >= 1e-3)   return (v * 1e3).toFixed(1) + 'ms';
  if (v >= 1e-6)   return (v * 1e6).toFixed(1) + 'µs';
  return (v * 1e9).toFixed(0) + 'ns';
}
