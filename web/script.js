// YarnPower statistics panel: fetches /yt/stat.json on load and when
// "Odśwież" (Refresh) is clicked. Titles reach the DOM only through textContent,
// and links are built only from video IDs.
'use strict';

(function (root) {
  const API_URL = '/yt/stat.json';
  const LOGIN_URL = '/yt/login';
  const WATCH_URL = 'https://www.youtube.com/watch?v=';
  const UNAVAILABLE = 'Niedostępne';

  const numberFormat = new Intl.NumberFormat('pl-PL');
  const dateFormat = new Intl.DateTimeFormat('pl-PL', {
    timeZone: 'Europe/Warsaw',
    day: 'numeric', month: 'long', year: 'numeric',
    hour: '2-digit', minute: '2-digit', second: '2-digit',
  });
  const shortDateFormat = new Intl.DateTimeFormat('pl-PL', {
    timeZone: 'Europe/Warsaw',
    day: '2-digit', month: '2-digit', year: 'numeric',
    hour: '2-digit', minute: '2-digit', second: '2-digit',
  });

  // --- DOM-free functions (tested in script.test.js) ---

  /** The number as pl-PL text, or null when the counter is unavailable. */
  function formatCount(value) {
    if (value === null || value === undefined || !/^\d+$/.test(String(value))) return null;
    return numberFormat.format(Number(value));
  }

  /** An ISO 8601 time in the Europe/Warsaw zone. */
  function formatTime(iso, short) {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return '—';
    return (short ? shortDateFormat : dateFormat).format(d);
  }

  function countValue(v) {
    return v === null || v === undefined || !/^\d+$/.test(String(v)) ? -1 : Number(v);
  }

  /** A copy of the list in the chosen order. Videos without a counter go last. */
  function sortVideos(videos, mode) {
    const list = videos.map((v, i) => ({ v, i }));
    if (mode === 'views') {
      list.sort((a, b) => countValue(b.v.viewCount) - countValue(a.v.viewCount) || a.i - b.i);
    }
    return list.map((x) => x.v);
  }

  function videoURL(id) {
    return WATCH_URL + encodeURIComponent(String(id));
  }

  /** Views as a percentage of the most popular video (0–100). */
  function shareOf(value, max) {
    const n = countValue(value);
    if (n <= 0 || max <= 0) return 0;
    return Math.max(1, Math.round((n / max) * 100));
  }

  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { formatCount, formatTime, sortVideos, videoURL, shareOf };
    return;
  }

  // --- browser ---

  const doc = root.document;
  const $ = (id) => doc.getElementById(id);
  const els = {
    refresh: $('refresh'),
    refreshLabel: doc.querySelector('#refresh .btn-label'),
    updateTime: $('update-time'),
    staleBadge: $('stale-badge'),
    status: $('status'),
    statusText: $('status-text'),
    statusRetry: $('status-retry'),
    subscribers: $('subscribers'),
    views: $('views'),
    videoCount: $('video-count'),
    rows: $('video-rows'),
    empty: $('empty'),
    sortButtons: doc.querySelectorAll('[data-sort]'),
  };

  let data = null;
  let sortMode = 'playlist';
  let loading = false;

  function setLoading(on) {
    loading = on;
    els.refresh.disabled = on;
    els.refresh.classList.toggle('is-loading', on);
    els.refreshLabel.textContent = on ? 'Odświeżanie…' : 'Odśwież';
    els.refresh.setAttribute('aria-busy', on ? 'true' : 'false');
  }

  function showStatus(kind, text, retry) {
    if (!kind) {
      els.status.hidden = true;
      els.statusText.textContent = '';
      return;
    }
    els.status.className = 'banner banner-' + kind;
    els.statusText.textContent = text;
    els.statusRetry.hidden = !retry;
    els.status.hidden = false;
  }

  function setCount(el, value) {
    const text = formatCount(value);
    el.textContent = text === null ? UNAVAILABLE : text;
    el.classList.toggle('is-unavailable', text === null);
  }

  function renderRows() {
    const videos = sortVideos(data.videos || [], sortMode);
    const max = videos.reduce((m, v) => Math.max(m, countValue(v.viewCount)), 0);
    const frag = doc.createDocumentFragment();

    videos.forEach((v, i) => {
      const tr = doc.createElement('tr');

      const rank = doc.createElement('td');
      rank.className = 'col-rank';
      rank.textContent = String(i + 1);

      const title = doc.createElement('td');
      const a = doc.createElement('a');
      a.className = 'video-link';
      a.href = videoURL(v.videoId);
      a.target = '_blank';
      a.rel = 'noopener noreferrer';
      a.textContent = v.title || '(bez tytułu)';
      const share = doc.createElement('span');
      share.className = 'share';
      share.setAttribute('aria-hidden', 'true');
      const fill = doc.createElement('span');
      fill.className = 'share-fill';
      fill.style.width = shareOf(v.viewCount, max) + '%';
      share.appendChild(fill);
      title.append(a, share);

      const count = doc.createElement('td');
      count.className = 'col-num';
      setCount(count, v.viewCount);

      tr.append(rank, title, count);
      frag.appendChild(tr);
    });

    els.rows.replaceChildren(frag);
    els.empty.hidden = videos.length > 0;
  }

  function render() {
    setCount(els.subscribers, data.stats && data.stats.subscriberCount);
    setCount(els.views, data.stats && data.stats.viewsCount);
    els.videoCount.textContent = numberFormat.format((data.videos || []).length);
    els.videoCount.classList.remove('is-unavailable');

    els.updateTime.textContent = formatTime(data.update_time, true);
    els.updateTime.dateTime = data.update_time || '';
    els.staleBadge.hidden = !data.stale;

    renderRows();

    if (data.stale) {
      showStatus('warn', 'Nie udało się pobrać nowych danych z YouTube. Pokazuję ostatni zapisany wynik z ' +
        formatTime(data.update_time) + '.', true);
    } else {
      showStatus(null);
    }
  }

  function showFailure(message) {
    if (data) {
      showStatus('error', message + ' Pokazuję wcześniej pobrane dane.', true);
      return;
    }
    // No data: remove the skeletons so they do not pretend to be loading.
    els.rows.replaceChildren();
    for (const el of [els.subscribers, els.views, els.videoCount]) {
      el.textContent = '—';
      el.classList.add('is-unavailable');
    }
    showStatus('error', message, true);
  }

  async function load() {
    if (loading) return;
    setLoading(true);
    if (!data) showStatus(null);
    try {
      const res = await fetch(API_URL, {
        credentials: 'same-origin',
        cache: 'no-store',
        headers: { Accept: 'application/json' },
      });
      if (res.status === 401) {
        root.location.assign(LOGIN_URL);
        return;
      }
      let body = null;
      try {
        body = await res.json();
      } catch (e) {
        body = null;
      }
      if (!res.ok || !body || typeof body !== 'object' || !Array.isArray(body.videos)) {
        const msg = body && typeof body.error === 'string' ? body.error : 'Serwer zwrócił błąd (' + res.status + ').';
        showFailure(msg);
        return;
      }
      data = body;
      render();
    } catch (e) {
      showFailure('Brak połączenia z serwerem. Sprawdź internet i spróbuj ponownie.');
    } finally {
      setLoading(false);
    }
  }

  function setSort(mode) {
    sortMode = mode;
    els.sortButtons.forEach((b) => b.setAttribute('aria-pressed', String(b.dataset.sort === mode)));
    if (data) renderRows();
  }

  els.refresh.addEventListener('click', load);
  els.statusRetry.addEventListener('click', load);
  els.sortButtons.forEach((b) => b.addEventListener('click', () => setSort(b.dataset.sort)));

  load();
})(typeof window !== 'undefined' ? window : globalThis);
