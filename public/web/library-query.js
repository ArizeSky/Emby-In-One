/* Emby-in-One Web client — shared library listing query builder (V1.4.4-rc2).
 *
 * One frozen source of truth for the `/Users/{uid}/Items` parameters of a
 * complete-library listing. The library page (paging + sort/filter) and the
 * home preview rows (first page only) both build their request from here, so
 * the two data channels can never drift apart.
 *
 * Pure: no Vue, no network, no auth, no module state. `pageParams` returns a
 * brand-new plain object and never reads or mutates anything else.
 */
(function () {
  'use strict';

  const PAGE_LIMIT = 50;
  const FIELDS = 'PrimaryImageAspectRatio,ProductionYear,ProviderIds,CommunityRating,RunTimeTicks,UserData';

  // `sort` is always "Field,Direction": a missing direction intentionally
  // yields SortOrder === undefined, exactly like the pre-refactor library page.
  // `filter` is the genre filter and stays an empty string when unused, so
  // EIOData.query() drops both it and an empty IncludeItemTypes from the URL.
  function pageParams({
    id, type, sort = 'DateCreated,Descending', filter = '', limit = PAGE_LIMIT, startIndex = 0,
  } = {}) {
    const [sortField, sortDir] = sort.split(',');
    return {
      ParentId: id,
      Limit: limit,
      SortBy: sortField,
      SortOrder: sortDir,
      Genres: filter,
      Fields: FIELDS,
      Recursive: true,
      IncludeItemTypes: type === 'tvshows' ? 'Series' : type === 'movies' ? 'Movie' : '',
      StartIndex: startIndex,
    };
  }

  window.EIOLibraryQuery = { PAGE_LIMIT, FIELDS, pageParams };
})();
