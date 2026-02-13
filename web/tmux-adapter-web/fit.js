// Custom terminal fit logic with minimum column enforcement via scaleX.
// No dependencies — works with any terminal that exposes cols/rows/resize().

export function getTerminalScreen(el) {
  if (!el) return null;
  return el.querySelector('.xterm-screen') || el.querySelector('canvas');
}

// Fit a terminal to its container, enforcing a minimum column count.
// When natural columns drop below minCols, the host is scaled horizontally
// via CSS transform so TUI apps never see fewer than minCols columns.
//
// Parameters:
//   term      - ghostty-web Terminal instance (needs cols, rows, resize())
//   container - the outer element whose clientWidth/clientHeight define bounds
//   host      - the inner element that wraps the terminal (gets transform applied)
//   minCols   - minimum column count (default 80)
export function fitTerminal(term, container, host, minCols) {
  if (minCols === undefined) minCols = 80;

  if (!container.clientWidth || !container.clientHeight) return;

  // Reset any width scaling before measuring available columns.
  host.style.width = '100%';
  host.style.transform = 'none';

  // Measure cell dimensions from the rendered terminal screen
  var screen = getTerminalScreen(host);
  if (!screen || !screen.offsetWidth || !screen.offsetHeight) return;

  var cellWidth = screen.offsetWidth / term.cols;
  var cellHeight = screen.offsetHeight / term.rows;

  if (cellWidth <= 0 || cellHeight <= 0) return;

  var naturalCols = Math.max(2, Math.floor(container.clientWidth / cellWidth));
  var newCols = naturalCols < minCols ? minCols : naturalCols;
  var newRows = Math.max(1, Math.floor(container.clientHeight / cellHeight));

  if (newCols !== term.cols || newRows !== term.rows) {
    term.resize(newCols, newRows);
  }

  // Keep minimum readable layout width by scaling width only.
  // This prevents downstream TUIs from breaking below their expected width.
  if (naturalCols < minCols) {
    var requiredWidth = minCols * cellWidth;
    if (requiredWidth > 0) {
      var scaleX = Math.min(1, container.clientWidth / requiredWidth);
      host.style.width = requiredWidth + 'px';
      host.style.transform = 'scaleX(' + scaleX + ')';
    }
  }
}
