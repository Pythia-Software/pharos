// Shared by the classic page scripts and the bundled React UI.
(function () {
  const cents = new Intl.NumberFormat(undefined, { style: 'currency', currency: 'USD', minimumFractionDigits: 2, maximumFractionDigits: 2 });
  const dollars = new Intl.NumberFormat(undefined, { style: 'currency', currency: 'USD', minimumFractionDigits: 0, maximumFractionDigits: 0 });
  function formatUSD(value) {
    if (!Number.isFinite(value)) return '';
    // A value that rounds up to $100 should already display whole dollars.
    const magnitude = Math.abs(value);
    return (magnitude >= 99.995 ? dollars : cents).format(magnitude < .005 ? 0 : value);
  }
  if (typeof module === 'object' && module.exports) module.exports = { formatUSD };
  else globalThis.pharosCurrency = { formatUSD };
})();
