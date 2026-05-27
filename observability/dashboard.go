package observability

import "net/http"

func registerDashboard(mux *http.ServeMux) {
	mux.HandleFunc("/dashboard", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(dashboardHTML))
	})
	// Redirect bare "/" to the dashboard.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/dashboard", http.StatusFound)
		} else {
			http.NotFound(w, r)
		}
	})
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1.0">
<title>go-agent · dashboard</title>
<script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.4/dist/chart.umd.min.js"></script>
<style>
:root{--bg:#0f1117;--surface:#1a1d27;--border:#2a2d3a;--text:#e2e8f0;--muted:#64748b;--green:#22c55e;--blue:#3b82f6;--purple:#a855f7;--orange:#f97316;--red:#ef4444;--yellow:#eab308;}
*{box-sizing:border-box;margin:0;padding:0;}
body{background:var(--bg);color:var(--text);font-family:Menlo,Monaco,'Courier New',monospace;font-size:13px;min-height:100vh;}
header{display:flex;align-items:center;justify-content:space-between;padding:14px 24px;border-bottom:1px solid var(--border);}
.logo{font-size:15px;font-weight:700;letter-spacing:-0.5px;}.logo em{color:var(--green);font-style:normal;}
.hdr-right{display:flex;align-items:center;gap:12px;color:var(--muted);font-size:11px;}
.dot{width:7px;height:7px;border-radius:50%;background:var(--green);animation:pulse 2s infinite;}
@keyframes pulse{0%,100%{opacity:1;}50%{opacity:.35;}}
.stats{display:grid;grid-template-columns:repeat(6,1fr);gap:1px;background:var(--border);border-bottom:1px solid var(--border);}
.stat{background:var(--surface);padding:14px 18px;}
.stat-lbl{color:var(--muted);font-size:10px;text-transform:uppercase;letter-spacing:.8px;margin-bottom:5px;}
.stat-val{font-size:20px;font-weight:600;letter-spacing:-.5px;}
.c{color:var(--green);}.t{color:var(--blue);}.r{color:var(--purple);}.tl{color:var(--orange);}.g{color:var(--red);}
.charts{display:grid;grid-template-columns:1fr 1fr;gap:1px;background:var(--border);}
.cbox{background:var(--surface);padding:18px 20px;}
.ctitle{color:var(--muted);font-size:10px;text-transform:uppercase;letter-spacing:.8px;margin-bottom:14px;}
.cwrap{position:relative;height:190px;}
</style>
</head>
<body>
<header>
  <div class="logo">go<em>agent</em></div>
  <div class="hdr-right">
    <div class="dot"></div>
    <span id="ts">connecting…</span>
    <span>· refreshes every 3s</span>
    <a href="/metrics" style="color:var(--muted);text-decoration:none;">raw /metrics</a>
  </div>
</header>

<div class="stats">
  <div class="stat"><div class="stat-lbl">Total Cost</div><div class="stat-val c" id="s0">$0.000000</div></div>
  <div class="stat"><div class="stat-lbl">Input Tokens</div><div class="stat-val t" id="s1">0</div></div>
  <div class="stat"><div class="stat-lbl">Output Tokens</div><div class="stat-val t" id="s2">0</div></div>
  <div class="stat"><div class="stat-lbl">Agent Runs</div><div class="stat-val r" id="s3">0</div></div>
  <div class="stat"><div class="stat-lbl">Tool Calls</div><div class="stat-val tl" id="s4">0</div></div>
  <div class="stat"><div class="stat-lbl">Guardrail Hits</div><div class="stat-val g" id="s5">0</div></div>
</div>

<div class="charts">
  <div class="cbox"><div class="ctitle">Cost by Model (USD)</div><div class="cwrap"><canvas id="cc"></canvas></div></div>
  <div class="cbox"><div class="ctitle">Token Usage by Model</div><div class="cwrap"><canvas id="ct"></canvas></div></div>
  <div class="cbox"><div class="ctitle">LLM Latency — p50 / p95 / p99 (seconds)</div><div class="cwrap"><canvas id="cl"></canvas></div></div>
  <div class="cbox"><div class="ctitle">Tool Calls (ok vs error)</div><div class="cwrap"><canvas id="ctl"></canvas></div></div>
  <div class="cbox"><div class="ctitle">Agent Run Status</div><div class="cwrap"><canvas id="cr"></canvas></div></div>
  <div class="cbox"><div class="ctitle">Guardrail Triggers by Name</div><div class="cwrap"><canvas id="cg"></canvas></div></div>
</div>

<script>
// ── Prometheus text-format parser ─────────────────────────────────────────────
function parse(txt) {
  const out = {};
  for (const raw of txt.split('\n')) {
    const line = raw.trim();
    if (!line || line.startsWith('#')) continue;
    const bi = line.indexOf('{');
    let name, lblStr, valStr;
    if (bi === -1) {
      const sp = line.indexOf(' ');
      name = line.slice(0, sp); lblStr = ''; valStr = line.slice(sp + 1);
    } else {
      name = line.slice(0, bi);
      const bj = line.indexOf('}', bi);
      lblStr = line.slice(bi + 1, bj);
      valStr = line.slice(bj + 2);
    }
    const v = parseFloat(valStr);
    if (isNaN(v)) continue;
    const labels = {};
    for (const m of lblStr.matchAll(/(\w+)="([^"]*)"/g)) labels[m[1]] = m[2];
    (out[name] = out[name] || []).push({ labels, v });
  }
  return out;
}

function rows(m, name, filter) {
  return (m[name] || []).filter(r =>
    !filter || Object.entries(filter).every(([k, v]) => r.labels[k] === v));
}
function total(m, name, filter) { return rows(m, name, filter).reduce((a, b) => a + b.v, 0); }
function uniq(m, name, key) { return [...new Set((m[name]||[]).map(r=>r.labels[key]).filter(Boolean))]; }

function pct(m, base, p, extra) {
  const bkts = rows(m, base + '_bucket', extra)
    .map(r => ({ le: r.labels.le === '+Inf' ? Infinity : +r.labels.le, v: r.v }))
    .sort((a, b) => a.le - b.le);
  if (!bkts.length) return 0;
  const tot = bkts.at(-1).v;
  if (!tot) return 0;
  const tgt = p * tot;
  let pLe = 0, pV = 0;
  for (const b of bkts) {
    if (b.v >= tgt) {
      if (b.v === pV) return pLe;
      const uLe = b.le === Infinity ? pLe * 2 || 1 : b.le;
      return pLe + (tgt - pV) / (b.v - pV) * (uLe - pLe);
    }
    pLe = b.le; pV = b.v;
  }
  return 0;
}

// ── Chart defaults ────────────────────────────────────────────────────────────
Chart.defaults.color = '#64748b';
Chart.defaults.borderColor = '#2a2d3a';
Chart.defaults.font.family = "Menlo,Monaco,'Courier New',monospace";
Chart.defaults.font.size = 11;

const C = ['#3b82f6','#22c55e','#a855f7','#f97316','#eab308','#ef4444','#06b6d4','#ec4899'];

function hbar(stacked) {
  return {
    indexAxis: 'y', responsive: true, maintainAspectRatio: false,
    plugins: { legend: stacked ? { labels: { color: '#94a3b8', boxWidth: 10 } } : { display: false } },
    scales: {
      x: { stacked, grid: { color: '#2a2d3a' }, ticks: { color: '#64748b' } },
      y: { stacked, grid: { color: '#2a2d3a' }, ticks: { color: '#94a3b8' } },
    },
  };
}
function vbar() {
  return {
    responsive: true, maintainAspectRatio: false,
    plugins: { legend: { labels: { color: '#94a3b8', boxWidth: 10 } } },
    scales: {
      x: { grid: { color: '#2a2d3a' }, ticks: { color: '#94a3b8' } },
      y: { grid: { color: '#2a2d3a' }, ticks: { color: '#64748b' } },
    },
  };
}

const ch = {};
function init(id, type, data, opts) {
  if (ch[id]) ch[id].destroy();
  ch[id] = new Chart(document.getElementById(id), { type, data, options: opts });
}
function upd(id, labels, datasets) {
  if (!ch[id]) return;
  ch[id].data.labels = labels;
  ch[id].data.datasets = datasets;
  ch[id].update('none');
}
function fmt(n) {
  if (n >= 1e6) return (n/1e6).toFixed(1)+'M';
  if (n >= 1e3) return (n/1e3).toFixed(1)+'k';
  return Math.round(n).toString();
}

function setup() {
  init('cc','bar',{labels:[],datasets:[{data:[],backgroundColor:C}]},hbar(false));
  init('ct','bar',{labels:[],datasets:[
    {label:'Input', data:[],backgroundColor:C[0]},
    {label:'Output',data:[],backgroundColor:C[1]},
  ]},hbar(true));
  init('cl','bar',{labels:[],datasets:[
    {label:'p50',data:[],backgroundColor:C[0]},
    {label:'p95',data:[],backgroundColor:C[2]},
    {label:'p99',data:[],backgroundColor:C[5]},
  ]},vbar());
  init('ctl','bar',{labels:[],datasets:[
    {label:'ok',   data:[],backgroundColor:C[1]},
    {label:'error',data:[],backgroundColor:C[5]},
  ]},hbar(true));
  init('cr','doughnut',{
    labels:['ok','error','max_steps'],
    datasets:[{data:[0,0,0],backgroundColor:[C[1],C[5],C[3]],borderWidth:0}]
  },{
    responsive:true,maintainAspectRatio:false,
    plugins:{legend:{position:'right',labels:{color:'#94a3b8',boxWidth:10}}},
  });
  init('cg','bar',{labels:[],datasets:[{data:[],backgroundColor:C}]},hbar(false));
}

async function refresh() {
  let txt;
  try { txt = await fetch('/metrics').then(r => r.text()); }
  catch { document.getElementById('ts').textContent = 'error: cannot reach /metrics'; return; }

  const m = parse(txt);

  // stat cards
  document.getElementById('s0').textContent = '$' + total(m,'goagent_llm_cost_usd_total').toFixed(6);
  document.getElementById('s1').textContent = fmt(total(m,'goagent_llm_tokens_total',{direction:'input'}));
  document.getElementById('s2').textContent = fmt(total(m,'goagent_llm_tokens_total',{direction:'output'}));
  document.getElementById('s3').textContent = fmt(total(m,'goagent_agent_runs_total'));
  document.getElementById('s4').textContent = fmt(total(m,'goagent_tool_calls_total'));
  document.getElementById('s5').textContent = fmt(total(m,'goagent_guardrail_triggers_total'));

  // cost by model
  const models = uniq(m,'goagent_llm_cost_usd_total','model');
  upd('cc', models, [{data:models.map(mo=>total(m,'goagent_llm_cost_usd_total',{model:mo})),backgroundColor:C}]);

  // tokens by model
  const tmod = uniq(m,'goagent_llm_tokens_total','model');
  upd('ct', tmod, [
    {label:'Input', data:tmod.map(mo=>total(m,'goagent_llm_tokens_total',{model:mo,direction:'input'})),  backgroundColor:C[0]},
    {label:'Output',data:tmod.map(mo=>total(m,'goagent_llm_tokens_total',{model:mo,direction:'output'})), backgroundColor:C[1]},
  ]);

  // latency percentiles
  const lmod = uniq(m,'goagent_llm_latency_seconds_bucket','model');
  upd('cl', lmod, [
    {label:'p50',data:lmod.map(mo=>pct(m,'goagent_llm_latency_seconds',.50,{model:mo})),backgroundColor:C[0]},
    {label:'p95',data:lmod.map(mo=>pct(m,'goagent_llm_latency_seconds',.95,{model:mo})),backgroundColor:C[2]},
    {label:'p99',data:lmod.map(mo=>pct(m,'goagent_llm_latency_seconds',.99,{model:mo})),backgroundColor:C[5]},
  ]);

  // tool calls
  const tools = uniq(m,'goagent_tool_calls_total','tool');
  upd('ctl', tools, [
    {label:'ok',   data:tools.map(t=>total(m,'goagent_tool_calls_total',{tool:t,status:'ok'})),    backgroundColor:C[1]},
    {label:'error',data:tools.map(t=>total(m,'goagent_tool_calls_total',{tool:t,status:'error'})), backgroundColor:C[5]},
  ]);

  // run status doughnut
  ch['cr'].data.datasets[0].data = [
    total(m,'goagent_agent_runs_total',{status:'ok'}),
    total(m,'goagent_agent_runs_total',{status:'error'}),
    total(m,'goagent_agent_runs_total',{status:'max_steps'}),
  ];
  ch['cr'].update('none');

  // guardrail triggers
  const gn = uniq(m,'goagent_guardrail_triggers_total','guardrail');
  upd('cg', gn, [{data:gn.map(g=>total(m,'goagent_guardrail_triggers_total',{guardrail:g})),backgroundColor:C}]);

  document.getElementById('ts').textContent = 'updated ' + new Date().toLocaleTimeString();
}

setup();
refresh();
setInterval(refresh, 3000);
</script>
</body>
</html>`
