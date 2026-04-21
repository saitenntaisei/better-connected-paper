import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useUrlSeed } from "./useUrlSeed";

describe("useUrlSeed", () => {
  beforeEach(() => {
    window.history.replaceState({}, "", "/");
  });
  afterEach(() => {
    window.history.replaceState({}, "", "/");
    vi.restoreAllMocks();
  });

  it("reads the current ?seed= param on mount", () => {
    window.history.replaceState({}, "", "/?seed=abc");
    const { result } = renderHook(() => useUrlSeed());
    expect(result.current.seed).toBe("abc");
  });

  it("updates the URL and local state on setSeed", () => {
    const { result } = renderHook(() => useUrlSeed());
    act(() => {
      result.current.setSeed("xyz");
    });
    expect(result.current.seed).toBe("xyz");
    expect(new URL(window.location.href).searchParams.get("seed")).toBe("xyz");
  });

  it("clears the seed param when set to null", () => {
    window.history.replaceState({}, "", "/?seed=xyz");
    const { result } = renderHook(() => useUrlSeed());
    act(() => {
      result.current.setSeed(null);
    });
    expect(result.current.seed).toBeNull();
    expect(new URL(window.location.href).searchParams.get("seed")).toBeNull();
  });

  it("pushes history on seed → seed transitions so Back returns to the prior graph", () => {
    const pushSpy = vi.spyOn(window.history, "pushState");
    const replaceSpy = vi.spyOn(window.history, "replaceState");
    const { result } = renderHook(() => useUrlSeed());

    act(() => {
      result.current.setSeed("abc");
    });
    expect(pushSpy).toHaveBeenCalledTimes(1);

    act(() => {
      result.current.setSeed("def");
    });
    expect(pushSpy).toHaveBeenCalledTimes(2);
    expect(result.current.seed).toBe("def");

    // re-setting the same id must not push a duplicate entry
    act(() => {
      result.current.setSeed("def");
    });
    expect(pushSpy).toHaveBeenCalledTimes(2);
    expect(replaceSpy).toHaveBeenCalled();
  });
});
