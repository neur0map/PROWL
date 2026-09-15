// Node 26 ships its own `localStorage` global that is undefined unless the
// process is started with --localstorage-file, and it wins over the one jsdom
// installs on the window. Any component that remembers a preference therefore
// crashed in tests while working in a browser. Installing a real in-memory
// Storage keeps those tests about the component.
if (typeof globalThis.localStorage === 'undefined' || globalThis.localStorage === null) {
  class MemoryStorage implements Storage {
    private entries = new Map<string, string>()

    get length() {
      return this.entries.size
    }

    clear() {
      this.entries.clear()
    }

    getItem(key: string) {
      return this.entries.get(key) ?? null
    }

    key(index: number) {
      return [...this.entries.keys()][index] ?? null
    }

    removeItem(key: string) {
      this.entries.delete(key)
    }

    setItem(key: string, value: string) {
      this.entries.set(key, String(value))
    }
  }

  const storage = new MemoryStorage()
  for (const target of [globalThis, globalThis.window].filter(Boolean)) {
    Object.defineProperty(target, 'localStorage', {
      value: storage,
      configurable: true,
      writable: true,
    })
  }
}
