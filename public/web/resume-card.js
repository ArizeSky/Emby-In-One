/* Emby-in-One Web client — continue-watching card view model.
 *
 * Pure presentation helper: no network, no auth and no mutation. Resume rows
 * stay the original episode objects (Id/UserData/runtime are untouched); this
 * module only derives the series-first link, title and subtitle for the card.
 */
(function () {
  'use strict';

  function text(value) {
    return typeof value === 'string' ? value.trim() : '';
  }

  function seriesIdOf(item) {
    if (!item || item.Type !== 'Episode') return '';
    return text(item.SeriesId);
  }

  function episodeNumber(value) {
    return typeof value === 'number' && Number.isInteger(value) && value >= 0 ? value : null;
  }

  function padded(value) {
    return String(value).padStart(2, '0');
  }

  function episodeSubtitle(item) {
    const season = episodeNumber(item.ParentIndexNumber);
    const episode = episodeNumber(item.IndexNumber);
    if (season != null && episode != null) return 'S' + padded(season) + 'E' + padded(episode);
    // Missing/non-finite indices must not invent a position or a total count.
    return text(item.Name) || text(item.SeriesName);
  }

  function resumeCardView(item) {
    const seriesId = seriesIdOf(item);
    if (seriesId) {
      return {
        title: text(item.SeriesName) || text(item.Name),
        href: '#/item/' + seriesId,
        subtitle: episodeSubtitle(item),
      };
    }
    // Movies, unknown item types and episodes without a usable SeriesId keep
    // the original card behaviour exactly: no SeriesId parsing, no padding and
    // no interpretation of the item.
    return {
      title: item && item.Name != null ? String(item.Name) : '',
      href: '#/item/' + (item && item.Id != null ? String(item.Id) : ''),
      subtitle: item && item.SeriesName ? String(item.SeriesName) : '',
    };
  }

  window.EIOResumeCard = { resumeCardView };
})();
