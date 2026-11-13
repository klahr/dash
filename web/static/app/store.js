// ------------------------------------------------------------------ browser settings (localStorage)
//
// Where storage is blocked (some private modes, sandboxed frames) every
// localStorage call throws, so each helper catches and falls back: a setting
// that can't be read is its default, one that can't be written is lost.
// No imports, so any module can use it while it loads.

// get returns the stored string, or fallback when it is missing or empty.
export function get(key, fallback = null) {
  try { return localStorage.getItem(key) || fallback; } catch { return fallback; }
}

// set stores value as a string; null or undefined removes the key. It
// reports whether the write worked (it fails when storage is full).
export function set(key, value) {
  try {
    if (value == null) localStorage.removeItem(key);
    else localStorage.setItem(key, String(value));
    return true;
  } catch { return false; }
}

export const remove = (key) => set(key, null);

// getJSON parses the stored value; missing or unparsable gives fallback.
export function getJSON(key, fallback = null) {
  try { return JSON.parse(localStorage.getItem(key)) ?? fallback; } catch { return fallback; }
}

export const setJSON = (key, value) => set(key, JSON.stringify(value));

// flag reads an on/off setting stored as "1"; setFlag writes it.
export const flag = (key) => get(key) === "1";
export const setFlag = (key, on) => set(key, on ? "1" : "0");
