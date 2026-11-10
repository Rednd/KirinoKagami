// KirinoKagami frontend: search and browse the mirror's beatmaps.
(() => {
  'use strict';

  const body = document.body;
  const COVER_BASE = (body.dataset.coverBase || '').replace(/\/+$/, '');
  // Not "data-preview-url": Go templates percent-encode attributes whose name
  // contains "url", which would turn the {id} placeholder into %7bid%7d.
  const PREVIEW_URL = body.dataset.previewPattern || '';
  const DOWNLOADS = body.dataset.downloads === 'true';
  const PAGE = 48;

  const STATUS_NAMES = { '-2': 'Graveyard', '-1': 'WIP', '0': 'Pending', '1': 'Ranked', '2': 'Approved', '3': 'Qualified', '4': 'Loved' };
  const STATUS_GROUPS = { any: [], ranked: [1, 2], qualified: [3], loved: [4], pending: [0, -1], graveyard: [-2] };
  const MODE_NAMES = ['osu!', 'osu!taiko', 'osu!catch', 'osu!mania'];
  const GENRES = { 1: 'Unspecified', 2: 'Video Game', 3: 'Anime', 4: 'Rock', 5: 'Pop', 6: 'Other', 7: 'Novelty', 9: 'Hip Hop', 10: 'Electronic', 11: 'Metal', 12: 'Classical', 13: 'Folk', 14: 'Jazz' };
  const LANGUAGES = { 1: 'Unspecified', 2: 'English', 3: 'Japanese', 4: 'Chinese', 5: 'Instrumental', 6: 'Korean', 7: 'French', 8: 'German', 9: 'Swedish', 10: 'Spanish', 11: 'Italian', 12: 'Russian', 13: 'Polish', 14: 'Other' };

  // Simple line-art mode glyphs (stroke = currentColor).
  const MODE_SVG = [
    '<circle cx="8" cy="8" r="6.2"/><circle cx="8" cy="8" r="2.4"/>',
    '<circle cx="8" cy="8" r="6.2"/><path d="M8 1.8v12.4"/>',
    '<circle cx="8" cy="10" r="4.2"/><path d="M8 5.8V2.2M8 2.2l2.4 1.4"/>',
    '<path d="M3 2v12M8 2v12M13 2v12"/>',
  ];
  const ICONS = {
    play: '<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M4 2.5v11l9-5.5z"/></svg>',
    pause: '<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M3.5 2.5h3v11h-3zM9.5 2.5h3v11h-3z"/></svg>',
    back: '<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M8 3.5v9L2.5 8zM14 3.5v9L8.5 8z"/></svg>',
    forward: '<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M2 3.5v9L7.5 8zM8 3.5v9L13.5 8z"/></svg>',
    download: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3v12M7 10l5 5 5-5M4 20h16"/></svg>',
    external: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M14 4h6v6M20 4l-9 9M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5"/></svg>',
    link: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1"/></svg>',
  };

  const $ = (sel) => document.querySelector(sel);
  const resultsEl = $('#results');
  const feedStatusEl = $('#feed-status');
  const summaryEl = $('#summary');
  const dialog = $('#detail');
  const input = $('#q');

  // ---------- helpers ----------

  // h('div', {class: 'x', onclick: fn}, child, 'text', ...) builds DOM safely.
  function h(tag, props, ...children) {
    const el = document.createElement(tag);
    for (const [k, v] of Object.entries(props || {})) {
      if (v == null || v === false) continue;
      if (k === 'class') el.className = v;
      else if (k === 'style') Object.assign(el.style, v);
      else if (k === 'dataset') Object.assign(el.dataset, v);
      else if (k === 'html') el.innerHTML = v; // only used with constant icon markup
      else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
      else el.setAttribute(k, v === true ? '' : v);
    }
    for (const c of children.flat()) {
      if (c == null || c === false) continue;
      el.append(c instanceof Node ? c : String(c));
    }
    return el;
  }

  function modeIcon(mode, title) {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('viewBox', '0 0 16 16');
    svg.setAttribute('class', 'mode-icon');
    svg.setAttribute('fill', 'none');
    svg.setAttribute('stroke', 'currentColor');
    svg.setAttribute('stroke-width', '1.6');
    svg.setAttribute('stroke-linecap', 'round');
    svg.setAttribute('role', 'img');
    svg.setAttribute('aria-label', title || MODE_NAMES[mode] || 'mode');
    svg.innerHTML = MODE_SVG[mode] || MODE_SVG[0];
    return svg;
  }

  const fmtNum = new Intl.NumberFormat();
  const fmtDate = new Intl.DateTimeFormat(undefined, { year: 'numeric', month: 'short', day: 'numeric' });

  function fmtLength(sec) {
    sec = Math.max(0, Math.round(sec || 0));
    const m = Math.floor(sec / 60), s = sec % 60;
    return `${m}:${String(s).padStart(2, '0')}`;
  }

  function fmtBPM(bpm) {
    return Number.isInteger(bpm) ? String(bpm) : bpm.toFixed(bpm >= 100 ? 0 : 1);
  }

  function validDate(s) {
    const d = new Date(s);
    return !isNaN(d) && d.getUTCFullYear() > 1970 ? d : null;
  }

  function timeAgo(date) {
    const sec = Math.round((Date.now() - date.getTime()) / 1000);
    const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });
    if (sec < 60) return rtf.format(-sec, 'second');
    if (sec < 3600) return rtf.format(-Math.round(sec / 60), 'minute');
    if (sec < 86400) return rtf.format(-Math.round(sec / 3600), 'hour');
    return rtf.format(-Math.round(sec / 86400), 'day');
  }

  // Star rating -> colour, interpolated along a blue -> green -> yellow ->
  // red -> purple -> black scale.
  const SR_STOPS = [
    [0.1, [66, 144, 251]], [1.25, [79, 192, 255]], [2.0, [79, 255, 213]], [2.5, [124, 255, 79]],
    [3.3, [246, 240, 92]], [4.2, [255, 128, 104]], [4.9, [255, 78, 111]], [5.8, [198, 69, 184]],
    [6.7, [101, 99, 222]], [7.7, [24, 21, 142]], [9.0, [0, 0, 0]],
  ];
  function srColor(sr) {
    if (!(sr >= 0.1)) return [170, 170, 170];
    if (sr >= SR_STOPS[SR_STOPS.length - 1][0]) return SR_STOPS[SR_STOPS.length - 1][1];
    for (let i = 1; i < SR_STOPS.length; i++) {
      const [x1, c1] = SR_STOPS[i];
      if (sr <= x1) {
        const [x0, c0] = SR_STOPS[i - 1];
        const t = (sr - x0) / (x1 - x0);
        return c0.map((v, j) => Math.round(v + (c1[j] - v) * t));
      }
    }
    return SR_STOPS[0][1];
  }
  function srStyle(sr) {
    const [r, g, b] = srColor(sr);
    const lum = (0.299 * r + 0.587 * g + 0.114 * b) / 255;
    return { '--sr': `rgb(${r} ${g} ${b})`, '--sr-text': sr >= 6.5 ? '#ffd966' : lum > 0.55 ? '#111' : '#fff' };
  }
  function applyVars(el, vars) {
    for (const [k, v] of Object.entries(vars)) el.style.setProperty(k, v);
    return el;
  }

  // Placeholder for sets without a cover image. The colours (pink / honey /
  // sky, light and dark versions) live in app.css; the tint is picked per set
  // so neighbouring cards differ.
  function fallbackCover(id) {
    return h('div', { class: 'card-nocover', 'aria-hidden': 'true', dataset: { tint: String(id % 5) } });
  }

  function coverImg(id, kind) {
    if (!COVER_BASE) return fallbackCover(id);
    const base = `${COVER_BASE}/${id}/covers/${kind}`;
    const img = h('img', {
      class: kind === 'card' ? 'card-cover' : null,
      src: `${base}.jpg`, srcset: `${base}.jpg 1x, ${base}@2x.jpg 2x`,
      alt: '', loading: 'lazy', decoding: 'async', referrerpolicy: 'no-referrer',
    });
    img.addEventListener('error', () => img.replaceWith(fallbackCover(id)), { once: true });
    return img;
  }

  function sortedDiffs(set) {
    return [...(set.ChildrenBeatmaps || [])].sort((a, b) => a.Mode - b.Mode || a.DifficultyRating - b.DifficultyRating);
  }

  function setStats(set) {
    const diffs = set.ChildrenBeatmaps || [];
    const bpms = diffs.map((d) => d.BPM).filter((b) => b > 0);
    return {
      bpm: bpms.length ? Math.max(...bpms) : 0,
      length: diffs.reduce((m, d) => Math.max(m, d.TotalLength || 0), 0),
    };
  }

  function statusBadge(status) {
    return h('span', { class: 'status', dataset: { status: String(status) } }, STATUS_NAMES[status] ?? 'Unknown');
  }

  // ---------- audio preview player ----------
  //
  // One shared <audio>. Card and detail buttons play / pause / resume a set's
  // preview; a player bar (and the same controls inside the detail view)
  // adds seeking, 5-second skips and a close button.

  const audio = new Audio();
  audio.preload = 'none';
  audio.volume = 0.5;
  const player = { id: null, set: null, seeking: false, error: false };
  const SKIP = 5;

  function previewURL(id) {
    return PREVIEW_URL ? PREVIEW_URL.replace('{id}', id) : '';
  }

  // A failed load leaves audio.paused false, so check the error flag too.
  const isPlaying = (id) => player.id === id && !player.error && !audio.paused && !audio.ended;
  const fmtTime = (t) => fmtLength(Math.floor(Number.isFinite(t) ? t : 0));

  function playPreview(set) {
    if (player.id === set.SetID && !player.error) {
      if (audio.paused || audio.ended) audio.play().catch(onPlayError);
      else audio.pause();
      return;
    }
    Object.assign(player, { id: set.SetID, set, seeking: false, error: false });
    audio.src = previewURL(set.SetID);
    audio.play().catch(onPlayError);
    renderBarMeta();
    syncPlayer();
  }

  function stopPreview() {
    audio.pause();
    audio.removeAttribute('src');
    audio.load();
    Object.assign(player, { id: null, set: null, seeking: false, error: false });
    syncPlayer();
  }

  function skip(seconds) {
    if (!player.id || !Number.isFinite(audio.duration)) return;
    audio.currentTime = Math.min(Math.max(0, audio.currentTime + seconds), audio.duration);
  }

  function onPlayError(err) {
    if (err && err.name === 'AbortError') return; // a newer preview replaced this one
    player.error = true;
    syncPlayer();
  }

  for (const evt of ['play', 'pause', 'ended', 'timeupdate', 'durationchange', 'loadedmetadata', 'seeked']) {
    audio.addEventListener(evt, () => syncPlayer());
  }
  audio.addEventListener('error', () => { if (player.id) onPlayError(); });

  // Play / pause / resume button for a set (icon-only on cards, labelled in
  // the detail view).
  function previewButton(set, withLabel) {
    if (!PREVIEW_URL) return null;
    return h('button', {
      type: 'button', class: withLabel ? 'btn' : 'play', 'aria-pressed': 'false',
      'aria-label': withLabel ? null : 'Play audio preview', dataset: { preview: String(set.SetID) },
      onclick: (e) => { e.stopPropagation(); playPreview(set); },
    }, h('span', { class: 'icon', html: isPlaying(set.SetID) ? ICONS.pause : ICONS.play }),
      withLabel ? h('span', { class: 'label' }, 'Preview') : null);
  }

  // Transport controls: skip back, play/pause, skip forward, time, seek bar.
  // forSet = only show while that set is the one loaded (detail view);
  // without it the controls follow whatever is loaded (player bar).
  function playerControls(forSet) {
    const toggle = h('button', {
      type: 'button', class: 'player-btn player-toggle', 'aria-label': 'Pause preview',
      onclick: () => { if (player.set) playPreview(player.set); },
    });
    const back = h('button', { type: 'button', class: 'player-btn', 'aria-label': `Back ${SKIP} seconds`, title: `Back ${SKIP}s`, html: ICONS.back, onclick: () => skip(-SKIP) });
    const fwd = h('button', { type: 'button', class: 'player-btn', 'aria-label': `Forward ${SKIP} seconds`, title: `Forward ${SKIP}s`, html: ICONS.forward, onclick: () => skip(SKIP) });
    const cur = h('span', { class: 'player-time player-cur' }, '0:00');
    const dur = h('span', { class: 'player-time player-dur' }, '0:00');
    const seek = h('input', { type: 'range', class: 'player-seek', min: '0', max: '0', step: '0.1', value: '0', 'aria-label': 'Seek preview' });
    // While dragging, only move the label; seek when the thumb is released
    // (keyboard arrows fire both events, so they seek immediately).
    seek.addEventListener('input', () => { player.seeking = true; cur.textContent = fmtTime(Number(seek.value)); paintSeek(seek); });
    seek.addEventListener('change', () => {
      if (Number.isFinite(audio.duration)) audio.currentTime = Number(seek.value);
      player.seeking = false;
      syncPlayer();
    });
    return h('div', { class: 'player-controls', dataset: forSet ? { set: String(forSet) } : {} },
      h('div', { class: 'player-buttons' }, back, toggle, fwd),
      cur, seek, dur);
  }

  function paintSeek(seek) {
    const max = Number(seek.max) || 0;
    seek.style.setProperty('--progress', `${max ? (Number(seek.value) / max) * 100 : 0}%`);
  }

  // Player bar fixed to the bottom of the page.
  const barThumb = h('span', { class: 'player-thumb', 'aria-hidden': 'true' });
  const barTitle = h('span', { class: 'player-title' });
  const barArtist = h('span', { class: 'player-artist' });
  const bar = h('div', { class: 'player-bar', role: 'region', 'aria-label': 'Audio preview', hidden: true },
    h('div', { class: 'player-bar-inner' },
      h('button', {
        type: 'button', class: 'player-meta', title: 'Show beatmap details',
        onclick: () => { if (player.set) openSet(player.set); },
      }, barThumb, h('span', { class: 'player-text' }, barTitle, barArtist)),
      playerControls(null),
      h('button', { type: 'button', class: 'player-btn player-close', 'aria-label': 'Close preview player', title: 'Close', onclick: stopPreview }, '×')));
  document.body.append(bar);

  function renderBarMeta() {
    const set = player.set;
    if (!set) return;
    barThumb.replaceChildren(coverImg(set.SetID, 'list'));
    barTitle.textContent = set.Title;
    barArtist.textContent = set.Artist;
  }

  function syncPlayer() {
    // Card and detail play buttons.
    for (const btn of document.querySelectorAll('[data-preview]')) {
      const id = Number(btn.dataset.preview);
      const active = player.id === id && !player.error;
      const on = isPlaying(id);
      const text = on ? 'Pause preview' : active ? 'Resume preview' : 'Play audio preview';
      btn.classList.toggle('playing', active);
      btn.setAttribute('aria-pressed', String(on));
      const icon = btn.querySelector('.icon');
      if (icon) icon.innerHTML = on ? ICONS.pause : ICONS.play;
      const label = btn.querySelector('.label');
      if (label) label.textContent = on ? 'Pause preview' : active ? 'Resume preview' : player.id === id ? 'Preview unavailable' : 'Preview';
      else btn.setAttribute('aria-label', text);
    }

    // Transport controls (player bar + detail view).
    const duration = Number.isFinite(audio.duration) ? audio.duration : 0;
    const playing = player.id != null && isPlaying(player.id);
    for (const c of document.querySelectorAll('.player-controls')) {
      c.hidden = player.id == null || (c.dataset.set != null && Number(c.dataset.set) !== player.id);
      const toggle = c.querySelector('.player-toggle');
      toggle.innerHTML = playing ? ICONS.pause : ICONS.play;
      toggle.setAttribute('aria-label', playing ? 'Pause preview' : 'Play preview');
      const seek = c.querySelector('.player-seek');
      seek.max = String(duration);
      seek.disabled = !duration;
      if (!player.seeking) {
        seek.value = String(audio.currentTime || 0);
        c.querySelector('.player-cur').textContent = fmtTime(audio.currentTime);
      }
      paintSeek(seek);
      c.querySelector('.player-dur').textContent = player.error ? 'unavailable' : fmtTime(duration);
      c.classList.toggle('has-error', player.error);
    }

    bar.hidden = player.id == null;
    document.body.classList.toggle('has-player', player.id != null);
  }

  // ---------- cards ----------

  const setCache = new Map();

  function diffDots(set) {
    const byMode = new Map();
    for (const d of sortedDiffs(set)) {
      if (!byMode.has(d.Mode)) byMode.set(d.Mode, []);
      byMode.get(d.Mode).push(d);
    }
    const MAX = byMode.size > 1 ? 8 : 14;
    return h('div', { class: 'diffs' }, [...byMode].map(([mode, diffs]) =>
      h('span', { class: 'diff-group', title: `${MODE_NAMES[mode]}: ${diffs.length} difficult${diffs.length === 1 ? 'y' : 'ies'}` },
        modeIcon(mode),
        diffs.slice(0, MAX).map((d) => applyVars(h('span', { class: 'dot', title: `${d.DiffName} (${d.DifficultyRating.toFixed(2)}★)` }), srStyle(d.DifficultyRating))),
        diffs.length > MAX ? h('span', { class: 'dot-more' }, `+${diffs.length - MAX}`) : null)));
  }

  function card(set) {
    const { bpm, length } = setStats(set);
    return h('article', { class: 'card' },
      coverImg(set.SetID, 'card'),
      previewButton(set, false),
      h('div', { class: 'card-body' },
        h('h3', { class: 'card-title' },
          h('button', { type: 'button', class: 'card-open', onclick: () => openSet(set) }, set.Title)),
        h('div', { class: 'card-artist' }, set.Artist),
        h('div', { class: 'card-mapper' }, 'mapped by ', h('strong', {}, set.Creator)),
        h('div', { class: 'card-foot' },
          statusBadge(set.RankedStatus),
          set.HasVideo ? h('span', { class: 'tag', title: 'Has video' }, 'Video') : null,
          diffDots(set),
          h('span', { class: 'card-meta' },
            bpm ? h('span', { title: 'BPM' }, `${fmtBPM(bpm)} BPM`) : null,
            length ? h('span', { title: 'Length' }, fmtLength(length)) : null,
            h('span', { title: 'Favourites' }, `♥ ${fmtNum.format(set.Favourites || 0)}`)))));
  }

  function skeletons(n) {
    return Array.from({ length: n }, () => h('div', { class: 'card skeleton', 'aria-hidden': 'true' }));
  }

  // ---------- search state ----------

  const state = { q: '', status: 'any', mode: -1, offset: 0, count: 0, done: false, loading: false };
  let ctrl = null;

  function readURL() {
    const p = new URLSearchParams(location.search);
    state.q = p.get('q') || '';
    state.status = STATUS_GROUPS[p.get('status')] ? p.get('status') : 'any';
    const m = Number(p.get('mode'));
    state.mode = p.has('mode') && m >= 0 && m <= 3 ? m : -1;
    return p.get('set');
  }

  function writeURL(setId, push) {
    const p = new URLSearchParams();
    if (state.q) p.set('q', state.q);
    if (state.status !== 'any') p.set('status', state.status);
    if (state.mode >= 0) p.set('mode', state.mode);
    if (setId) p.set('set', setId);
    const url = `${location.pathname}${p.size ? `?${p}` : ''}`;
    if (push) history.pushState({ set: setId || null }, '', url);
    else history.replaceState({ set: setId || null }, '', url);
  }

  function syncControls() {
    input.value = state.q;
    for (const btn of document.querySelectorAll('#status-filter button')) {
      btn.setAttribute('aria-checked', String(btn.dataset.value === state.status));
    }
    for (const btn of document.querySelectorAll('#mode-filter button')) {
      btn.setAttribute('aria-checked', String(Number(btn.dataset.value) === state.mode));
    }
  }

  function describe() {
    const parts = [];
    if (state.status !== 'any') parts.push(document.querySelector(`#status-filter [data-value="${state.status}"]`).textContent.toLowerCase());
    if (state.mode >= 0) parts.push(MODE_NAMES[state.mode]);
    const noun = state.done && state.count === 1 ? 'beatmap' : 'beatmaps';
    const what = parts.length ? `${parts.join(' ')} ${noun}` : noun;
    return state.q ? `${what} matching “${state.q}”` : `newest ${what}`;
  }

  // One persistent button, so keyboard focus stays on it between pages.
  const loadMoreBtn = h('button', { type: 'button', class: 'btn load-more', onclick: () => load(false) }, 'Load more');

  function renderFeedStatus(error) {
    // aria-disabled rather than disabled: disabling a focused button would
    // drop keyboard focus. load() already ignores clicks while loading.
    loadMoreBtn.setAttribute('aria-disabled', String(state.loading));
    loadMoreBtn.textContent = state.loading ? 'Loading…' : 'Load more';
    if (!error && !state.done && state.count > 0) {
      if (feedStatusEl.firstChild !== loadMoreBtn || feedStatusEl.childNodes.length > 1) {
        feedStatusEl.replaceChildren(loadMoreBtn);
      }
      return;
    }
    feedStatusEl.replaceChildren();
    if (error) {
      feedStatusEl.append(
        h('span', { class: 'empty-title' }, 'Couldn’t load beatmaps'),
        h('span', {}, error),
        h('br'),
        h('button', { type: 'button', class: 'btn retry', onclick: () => load(state.count === 0) }, 'Try again'));
    } else if (state.done && state.count === 0) {
      feedStatusEl.append(
        h('span', { class: 'empty-title' }, 'No beatmaps found'),
        h('span', {}, state.q ? 'Try fewer or different words, or loosen the filters.' : 'Nothing matches these filters yet.'));
    } else if (state.done) {
      feedStatusEl.textContent = 'That’s everything.';
    }
  }

  async function load(reset) {
    if (reset) {
      if (ctrl) ctrl.abort();
      ctrl = null;
      Object.assign(state, { offset: 0, count: 0, done: false, loading: false });
      resultsEl.replaceChildren(...skeletons(12));
      summaryEl.textContent = '';
      feedStatusEl.replaceChildren();
    }
    if (state.loading || state.done) return;
    state.loading = true;
    const c = (ctrl = new AbortController());

    const p = new URLSearchParams({ amount: PAGE, offset: state.offset });
    if (state.q) p.set('query', state.q);
    for (const s of STATUS_GROUPS[state.status]) p.append('status', s);
    if (state.mode >= 0) p.set('mode', state.mode);

    if (!reset) {
      resultsEl.append(...skeletons(4));
      renderFeedStatus();
    }
    try {
      const res = await fetch(`/api/search?${p}`, { signal: c.signal });
      if (!res.ok) throw new Error(`The mirror answered ${res.status}.`);
      const sets = (await res.json()) || [];
      if (c !== ctrl) return;
      resultsEl.querySelectorAll('.skeleton').forEach((el) => el.remove());
      for (const set of sets) {
        setCache.set(set.SetID, set);
        resultsEl.append(card(set));
      }
      state.offset += sets.length;
      state.count += sets.length;
      syncPlayer();
      if (sets.length < PAGE) state.done = true;
      summaryEl.textContent = `${fmtNum.format(state.count)}${state.done ? '' : '+'} ${describe()}`;
      state.loading = false;
      renderFeedStatus();
    } catch (err) {
      if (err.name === 'AbortError' || c !== ctrl) return;
      resultsEl.querySelectorAll('.skeleton').forEach((el) => el.remove());
      state.loading = false;
      renderFeedStatus(err.message || 'Network error.');
    }
  }

  function applyFilters() {
    writeURL(null, false);
    syncControls();
    load(true);
  }

  let debounce;
  input.addEventListener('input', () => {
    clearTimeout(debounce);
    debounce = setTimeout(() => {
      const q = input.value.trim();
      if (q === state.q) return;
      state.q = q;
      applyFilters();
    }, 300);
  });
  $('#search-form').addEventListener('submit', (e) => {
    e.preventDefault();
    clearTimeout(debounce);
    state.q = input.value.trim();
    applyFilters();
    input.blur();
  });

  $('#status-filter').addEventListener('click', (e) => {
    const btn = e.target.closest('button');
    if (!btn || btn.dataset.value === state.status) return;
    state.status = btn.dataset.value;
    applyFilters();
  });
  $('#mode-filter').addEventListener('click', (e) => {
    const btn = e.target.closest('button');
    if (!btn || Number(btn.dataset.value) === state.mode) return;
    state.mode = Number(btn.dataset.value);
    applyFilters();
  });

  // ---------- light / dark theme ----------

  const themeBtn = $('#theme-toggle');
  function setTheme(theme, save) {
    if (theme === 'dark') document.documentElement.dataset.theme = 'dark';
    else delete document.documentElement.dataset.theme;
    const label = theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode';
    themeBtn.setAttribute('aria-label', label);
    themeBtn.title = label;
    if (save) {
      try { localStorage.setItem('kirino-theme', theme); } catch { /* private mode etc. */ }
    }
  }
  setTheme(document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light', false);
  themeBtn.addEventListener('click', () => {
    setTheme(document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark', true);
  });

  document.addEventListener('keydown', (e) => {
    if (e.key === '/' && document.activeElement !== input && !dialog.open) {
      e.preventDefault();
      input.focus();
    }
  });

  // ---------- detail dialog ----------

  function fact(label, value) {
    return value == null || value === '' ? null : h('div', {}, h('dt', {}, label), h('dd', {}, value));
  }

  function diffTable(set) {
    const dash = '–';
    return h('div', { class: 'table-wrap' },
      h('table', { class: 'diff-table' },
        h('thead', {}, h('tr', {},
          ['Difficulty', 'Stars', 'CS', 'AR', 'OD', 'HP', 'BPM', 'Length', 'Combo', 'Plays'].map((t) => h('th', { scope: 'col' }, t)))),
        h('tbody', {}, sortedDiffs(set).map((d) => {
          const mania = d.Mode === 3, taiko = d.Mode === 1;
          return h('tr', {},
            h('td', {}, h('span', { class: 'diff-name' }, modeIcon(d.Mode), d.DiffName)),
            h('td', {}, applyVars(h('span', { class: 'sr' }, `${d.DifficultyRating.toFixed(2)}★`), srStyle(d.DifficultyRating))),
            h('td', {}, taiko ? dash : mania ? `${d.CS}K` : d.CS),
            h('td', {}, taiko || mania ? dash : d.AR),
            h('td', {}, d.OD),
            h('td', {}, d.HP),
            h('td', {}, d.BPM ? fmtBPM(d.BPM) : dash),
            h('td', {}, fmtLength(d.TotalLength)),
            h('td', {}, d.MaxCombo ? fmtNum.format(d.MaxCombo) : dash),
            h('td', {}, fmtNum.format(d.Playcount || 0)));
        }))));
  }

  function tagList(set) {
    const tags = [...new Set((set.Tags || '').split(/\s+/).filter(Boolean))].slice(0, 40);
    if (!tags.length) return null;
    return h('section', {},
      h('h3', { class: 'section-title' }, 'Tags'),
      h('div', { class: 'tags' }, tags.map((t) => h('button', {
        type: 'button', title: `Search for “${t}”`,
        onclick: () => { dialog.close(); state.q = t; applyFilters(); },
      }, t))));
  }

  function renderDetail(set) {
    const { bpm, length } = setStats(set);
    const approved = validDate(set.ApprovedDate);
    const updated = validDate(set.LastUpdate);
    const modes = [...new Set((set.ChildrenBeatmaps || []).map((d) => d.Mode))].sort();
    const osuURL = `https://osu.ppy.sh/beatmapsets/${set.SetID}`;
    const dateLabel = { 1: 'Ranked', 2: 'Approved', 3: 'Qualified', 4: 'Loved' }[set.RankedStatus];

    dialog.replaceChildren(
      h('div', { class: 'detail-hero' },
        coverImg(set.SetID, 'cover'),
        h('button', { type: 'button', class: 'detail-close', 'aria-label': 'Close', onclick: () => dialog.close() }, '×'),
        h('div', { class: 'card-foot' },
          statusBadge(set.RankedStatus),
          set.HasVideo ? h('span', { class: 'tag' }, 'Video') : null,
          modes.map((m) => h('span', { class: 'tag' }, MODE_NAMES[m]))),
        h('h2', { class: 'detail-title', id: 'detail-title' }, set.Title),
        h('div', { class: 'detail-sub' }, set.Artist, ' · mapped by ', h('strong', {}, set.Creator))),
      h('div', { class: 'detail-body' },
        h('div', { class: 'actions' },
          DOWNLOADS ? h('a', { class: 'btn btn-primary', href: `/d/${set.SetID}`, html: `${ICONS.download}<span>Download</span>` }) : null,
          DOWNLOADS && set.HasVideo ? h('a', { class: 'btn', href: `/d/${set.SetID}n`, html: `${ICONS.download}<span>Without video</span>` }) : null,
          previewButton(set, true),
          h('a', { class: 'btn', href: osuURL, target: '_blank', rel: 'noopener noreferrer', html: `${ICONS.external}<span>osu! website</span>` }),
          h('button', {
            type: 'button', class: 'btn', html: `${ICONS.link}<span>Copy link</span>`,
            onclick: (e) => {
              const btn = e.currentTarget;
              navigator.clipboard?.writeText(location.href).then(() => {
                btn.lastChild.textContent = 'Copied';
                setTimeout(() => { btn.lastChild.textContent = 'Copy link'; }, 1500);
              });
            },
          })),
        PREVIEW_URL ? playerControls(set.SetID) : null,
        h('dl', { class: 'facts' },
          fact('Set ID', String(set.SetID)),
          approved && dateLabel ? fact(dateLabel, fmtDate.format(approved)) : null,
          updated ? fact('Last updated', fmtDate.format(updated)) : null,
          fact('Source', set.Source),
          fact('Genre', GENRES[set.Genre]),
          fact('Language', LANGUAGES[set.Language]),
          fact('BPM', bpm ? fmtBPM(bpm) : null),
          fact('Length', length ? fmtLength(length) : null),
          fact('Favourites', fmtNum.format(set.Favourites || 0))),
        h('section', {},
          h('h3', { class: 'section-title' }, `Difficulties (${(set.ChildrenBeatmaps || []).length})`),
          diffTable(set)),
        tagList(set)));
  }

  let closingFromHistory = false;

  function openSet(set, fromHistory) {
    renderDetail(set);
    syncPlayer();
    if (!dialog.open) dialog.showModal();
    dialog.querySelector('.detail-body').scrollTop = 0;
    if (!fromHistory) writeURL(set.SetID, true);
  }

  async function openSetById(id, fromHistory) {
    const cached = setCache.get(id);
    if (cached) return openSet(cached, fromHistory);
    try {
      const res = await fetch(`/api/s/${id}`);
      if (!res.ok) throw new Error();
      const set = await res.json();
      if (!set) throw new Error();
      setCache.set(set.SetID, set);
      openSet(set, fromHistory);
    } catch {
      writeURL(null, false);
    }
  }

  dialog.addEventListener('close', () => {
    if (closingFromHistory) { closingFromHistory = false; return; }
    if (history.state && history.state.set) history.back();
    else writeURL(null, false);
  });

  // Close when clicking the backdrop.
  dialog.addEventListener('click', (e) => { if (e.target === dialog) dialog.close(); });

  window.addEventListener('popstate', () => {
    const before = `${state.q}|${state.status}|${state.mode}`;
    const id = Number(readURL());
    if (`${state.q}|${state.status}|${state.mode}` !== before) {
      syncControls();
      load(true);
    }
    if (id > 0) openSetById(id, true);
    else if (dialog.open) { closingFromHistory = true; dialog.close(); }
  });

  // ---------- footer ----------

  async function loadFooter() {
    const footer = $('#footer');
    const apiLink = h('a', { href: '/api/search' }, 'cheesegull-compatible API');
    footer.replaceChildren(apiLink);
    try {
      const res = await fetch('/api/status');
      if (!res.ok) return;
      const st = await res.json();
      const parts = [];
      if (st.database && st.database.sets != null) {
        parts.push(`${fmtNum.format(st.database.sets)} beatmap sets`, `${fmtNum.format(st.database.beatmaps)} difficulties`);
      }
      const built = st.search && validDate(st.search.built_at);
      if (built) parts.push(`search index updated ${timeAgo(built)}`);
      footer.replaceChildren(parts.join(' · '), parts.length ? ' · ' : '', apiLink);
    } catch { /* footer stats are optional */ }
  }

  // ---------- start ----------

  const initialSet = readURL();
  syncControls();
  history.replaceState({ set: null }, '', location.href);
  load(true);
  loadFooter();
  if (Number(initialSet) > 0) {
    // Put a history entry under the dialog so "back" closes it.
    writeURL(null, false);
    openSetById(Number(initialSet), false);
  }
})();
