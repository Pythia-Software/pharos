// Shared by the classic page scripts and the bundled React UI.
//
// Token counts show three significant figures, like dollar amounts show a
// fixed precision for their size: exact below 1,000 (with at most one decimal
// for averages), then K, M, B and T. A value that rounds up to the next unit
// already displays in it (999,500 is 1M, not 1,000K). Calculations, exports
// and hover titles keep exact counts.
(function () {
  const small = new Intl.NumberFormat('en', { maximumSignificantDigits: 3, maximumFractionDigits: 1, roundingPriority: 'lessPrecision' });
  const large = new Intl.NumberFormat('en', { notation: 'compact', maximumSignificantDigits: 3 });
  function formatTokens(value) {
    if (typeof value !== 'number' || !Number.isFinite(value)) return '';
    const magnitude = Math.abs(value);
    if (magnitude < .05) return '0';
    return (magnitude >= 999.5 ? large : small).format(value);
  }
  // "1.23M tokens", "1 token".
  function formatTokenCount(value) {
    const text = formatTokens(value);
    return text && `${text} ${text === '1' ? 'token' : 'tokens'}`;
  }
  const api = { formatTokens, formatTokenCount };
  if (typeof module === 'object' && module.exports) module.exports = api;
  else globalThis.pharosTokens = api;
})();
