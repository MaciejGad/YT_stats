// Testy funkcji bez DOM z web/script.js. Uruchomienie: node --test test/js
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { formatCount, formatTime, sortVideos, videoURL, shareOf } = require('../../web/script.js');

// Intl w pl-PL używa niełamliwej spacji jako separatora tysięcy.
const nb = (s) => s.replace(/[  ]/g, ' ');

test('formatCount formatuje według pl-PL', () => {
  assert.equal(nb(formatCount('82187')), '82 187');
  assert.equal(nb(formatCount('1234567')), '1 234 567');
  assert.equal(formatCount('0'), '0');
  assert.equal(formatCount('999'), '999');
});

test('formatCount zwraca null dla braku licznika', () => {
  for (const v of [null, undefined, '', '-1', '1e5', 'abc', ' 12']) {
    assert.equal(formatCount(v), null, String(v));
  }
});

test('formatTime pokazuje czas w Europe/Warsaw niezależnie od strefy', () => {
  // 23:15:30 UTC zimą to 00:15:30 następnego dnia w Polsce.
  const winter = nb(formatTime('2026-11-23T23:15:30Z'));
  assert.match(winter, /24 listopada 2026/);
  assert.match(winter, /00:15:30/);
  // Latem +02:00.
  assert.match(nb(formatTime('2026-07-01T10:00:00Z')), /12:00:00/);
  // Czas z przesunięciem z serwera.
  assert.match(nb(formatTime('2026-11-24T00:15:30+01:00')), /00:15:30/);
});

test('formatTime w wersji krótkiej do panelu podsumowania', () => {
  assert.equal(nb(formatTime('2026-11-23T23:15:30Z', true)), '24.11.2026, 00:15:30');
});

test('formatTime dla błędnej daty', () => {
  assert.equal(formatTime('nie data'), '—');
  assert.equal(formatTime(undefined), '—');
});

const videos = [
  { videoId: 'a', title: 'A', viewCount: '100' },
  { videoId: 'b', title: 'B', viewCount: null },
  { videoId: 'c', title: 'C', viewCount: '5000' },
  { videoId: 'd', title: 'D', viewCount: '100' },
];

test('sortVideos zachowuje kolejność playlisty', () => {
  assert.deepEqual(sortVideos(videos, 'playlist').map((v) => v.videoId), ['a', 'b', 'c', 'd']);
});

test('sortVideos po wyświetleniach: malejąco, stabilnie, brak licznika na końcu', () => {
  assert.deepEqual(sortVideos(videos, 'views').map((v) => v.videoId), ['c', 'a', 'd', 'b']);
});

test('sortVideos nie zmienia oryginalnej tablicy', () => {
  const copy = videos.map((v) => v.videoId);
  sortVideos(videos, 'views');
  assert.deepEqual(videos.map((v) => v.videoId), copy);
});

test('videoURL koduje identyfikator', () => {
  assert.equal(videoURL('-i13vpEByJM'), 'https://www.youtube.com/watch?v=-i13vpEByJM');
  assert.equal(videoURL('a&b=c#d'), 'https://www.youtube.com/watch?v=a%26b%3Dc%23d');
  assert.equal(videoURL('"><script>'), 'https://www.youtube.com/watch?v=%22%3E%3Cscript%3E');
});

test('shareOf liczy udział względem najpopularniejszego filmu', () => {
  assert.equal(shareOf('5000', 5000), 100);
  assert.equal(shareOf('2500', 5000), 50);
  assert.equal(shareOf('1', 5000), 1); // widoczny pasek nawet dla małych wartości
  assert.equal(shareOf('0', 5000), 0);
  assert.equal(shareOf(null, 5000), 0);
  assert.equal(shareOf('10', 0), 0);
});
