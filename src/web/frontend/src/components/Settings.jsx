/**
 * Settings.jsx
 *
 * Main app view after initial setup. Tabs: Home, Settings, Admin, Logs.
 * Each section fetches its own data directly from the API — no prop-drilling.
 *
 * Sections:
 *   HomeSection    — scheduled playlists, manual run, live output
 *   ConfigSection  — raw .env editor, wizard re-run, reset //Might drop this and replace it as it causes more *  confustion
 *   AdminSection   — CRUD management for database-backed config tables
 *   LogsSection    — full server log viewer
 */

import { useState, useEffect, useCallback, useRef } from "react";
import {
  fetchConfig,
  fetchConfigRaw,
  saveConfig,
  resetConfig,
  saveSchedule,
  startRun,
  stopRun,
  fetchRunStatus,
  fetchLogs,
  fetchCustomPlaylists,
  deleteCustomPlaylist,
  fetchAdminSchema,
  fetchAdminResource,
  createAdminResource,
  updateAdminResource,
  deleteAdminResource,
} from "../lib/api";
import { parseSlogLine, cronToFields, highlightEnv } from "../lib/utils";
import { fetchPlaylistTracks } from "../lib/listenbrainz";
import { motion, AnimatePresence } from "motion/react";
import { Toggle } from "./ui/Toggle";
import { Button, SectionLabel, Panel, LogRow, TextField } from "./ui/common";
import { DirInput } from "./ui/DirInput";
import { PlaylistCard, TracklistDropdown } from "./ui/PlaylistCard";
import { ImportModal } from "./ui/ImportModal";
import { UpdateNotification } from "./ui/UpdateNotification";
import { PathTemplateModal } from "./ui/PathTemplateModal";

const tabBtnCls = (active) =>
  `bg-transparent border-none border-b-2 pb-2 px-3.5 text-[13px] leading-none cursor-pointer transition-colors
  ${active ? "text-accent border-accent" : "text-muted border-transparent hover:text-white"}`;

// ── Home Tab ──────────────────────────────────────────────────────────────────
// Manages scheduled playlists, manual runs, and live run output.
// Fetches its own config on mount to initialise schedule state and locked keys.

// Streams live run output from /api/ui/run/events
function useSSE({ onLine, onDone }) {
  const abortRef = useRef(null);

  const connect = useCallback(async () => {
    if (abortRef.current) abortRef.current.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    try {
      const res = await fetch("/api/ui/run/events", {
        credentials: "include",
        signal: controller.signal,
      });
      if (!res.ok) {
        onDone(null);
        return;
      }
      const reader = res.body.getReader();
      const dec = new TextDecoder();
      let buf = "";
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += dec.decode(value, { stream: true });
        const parts = buf.split("\n\n");
        buf = parts.pop();
        for (const part of parts) {
          let ev = "",
            data = "";
          for (const l of part.split("\n")) {
            if (l.startsWith("event: ")) ev = l.slice(7).trim();
            if (l.startsWith("data: ")) data = l.slice(6);
          }
          if (ev === "done") {
            onDone(parseInt(data));
            return;
          } else if (data) onLine(data);
        }
      }
    } catch (e) {
      if (e.name !== "AbortError") onDone(null);
    } finally {
      if (abortRef.current === controller) abortRef.current = null;
    }
  }, [onLine, onDone]);

  const disconnect = useCallback(() => {
    abortRef.current?.abort();
    abortRef.current = null;
  }, []);

  return { connect, disconnect };
}

const PLAYLISTS = [
  {
    value: "weekly-exploration",
    name: "Weekly Exploration",
    scheduleKey: "WEEKLY_EXPLORATION_SCHEDULE",
    defaultDay: 2,
    defaultHour: 0,
    defaultMinute: 15,
  },
  {
    value: "weekly-jams",
    name: "Weekly Jams",
    scheduleKey: "WEEKLY_JAMS_SCHEDULE",
    defaultDay: 1,
    defaultHour: 0,
    defaultMinute: 30,
  },
  {
    value: "daily-jams",
    name: "Daily Jams",
    scheduleKey: "DAILY_JAMS_SCHEDULE",
    defaultDay: -1,
    defaultHour: 1,
    defaultMinute: 15,
  },
  {
    value: "on-repeat",
    name: "On Repeat",
    scheduleKey: "ON_REPEAT_SCHEDULE",
    defaultDay: 100,
    defaultHour: 12,
    defaultMinute: 0,
    fixedSchedule: true,
  },
];

const SCHEDULE_DAYS = [
  { value: -2, label: "Never", summary: "No schedule" },
  { value: -1, label: "Every day", summary: "Every day" },
  { value: 0, label: "Sunday", summary: "Every Sunday" },
  { value: 1, label: "Monday", summary: "Every Monday" },
  { value: 2, label: "Tuesday", summary: "Every Tuesday" },
  { value: 3, label: "Wednesday", summary: "Every Wednesday" },
  { value: 4, label: "Thursday", summary: "Every Thursday" },
  { value: 5, label: "Friday", summary: "Every Friday" },
  { value: 6, label: "Saturday", summary: "Every Saturday" },
  { value: 100, label: "Monthly (1st)", summary: "Every 1st of the month" },
];

const selectCls =
  "bg-surface border border-ui-border text-white rounded-[6px] px-2.5 py-1.5 text-[13px] cursor-pointer outline-none focus:border-accent";
const PATH_TEMPLATE_TOKENS = [
  "{{Artist}}",
  "{{Album}}",
  "{{Track}}",
  "{{File}}",
];

function TracklistSlide({ show, slideKey, children }) {
  return (
    <AnimatePresence>
      {show && (
        <motion.div
          key={slideKey}
          initial={{ opacity: 0, height: 0 }}
          animate={{ opacity: 1, height: "auto" }}
          exit={{ opacity: 0, height: 0 }}
          transition={{ duration: 0.28, ease: "easeInOut" }}
          style={{ overflow: "hidden" }}
        >
          {children}
        </motion.div>
      )}
    </AnimatePresence>
  );
}

function CustomPlaylistsSection({
  customPlaylists,
  schedules,
  scheduleProps,
  openTracklist,
  setOpenTracklist,
  lbUser,
  onImportedRefresh,
  onSync,
  onDelete,
  showImportModal,
  setShowImportModal,
}) {
  return (
    <div className="mt-6">
      <div
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
        }}
      >
        <div className="text-[16px] font-bold tracking-tight text-white">
          Custom Playlists
        </div>
        <button
          onClick={() => setShowImportModal(true)}
          className="bg-transparent border border-ui-border text-muted rounded-full px-3 py-1 text-[12px] cursor-pointer hover:text-white hover:border-[#444] transition-colors"
        >
          + Import
        </button>
      </div>

      {customPlaylists.length === 0 ? (
        <p className="text-[12px] text-muted mt-3">
          No custom playlists yet. Import one from ListenBrainz or Apple Music.
        </p>
      ) : (
        <div className="flex gap-3 mt-3 overflow-x-auto snap-x snap-mandatory pb-2">
          {customPlaylists.map((cp, i) => {
            if (!schedules[cp.id]) return null;
            return (
              <div
                key={cp.id}
                className="shrink-0 snap-start w-full min-[420px]:w-[calc((100%-12px)/2)] min-[720px]:w-[calc((100%-36px)/4)]"
              >
                <PlaylistCard
                  playlist={{
                    value: `custom-${cp.color_index ?? i}`,
                    name: cp.name,
                  }}
                  trackId={cp.id}
                  artworkUrl={cp.artwork_url || undefined}
                  {...scheduleProps(cp.id)}
                  index={i}
                  nextRunText={
                    schedules[cp.id]?.enabled
                      ? (SCHEDULE_DAYS.find(
                          (d) => d.value === schedules[cp.id].day,
                        )?.summary ?? "Every day")
                      : "Disabled"
                  }
                  tracklistOpen={openTracklist === cp.id}
                  onTracklistToggle={() =>
                    setOpenTracklist((v) => (v === cp.id ? null : cp.id))
                  }
                  onDelete={(opts) => onDelete(cp.id, opts)}
                />
              </div>
            );
          })}
        </div>
      )}

      <TracklistSlide
        show={openTracklist && openTracklist.startsWith("custom-")}
        slideKey={openTracklist}
      >
        <TracklistDropdown
          playlist={openTracklist}
          lbUser={null}
          onRun={() => onSync(openTracklist)}
        />
      </TracklistSlide>

      <AnimatePresence>
        {showImportModal && (
          <ImportModal
            onClose={() => setShowImportModal(false)}
            onImported={onImportedRefresh}
            onSync={onSync}
          />
        )}
      </AnimatePresence>
    </div>
  );
}

function HomeSection() {
  const [schedules, setSchedules] = useState(null);
  const [envSources, setEnvSources] = useState({});
  const [scheduleSaveStatus, setScheduleSaveStatus] = useState({});
  const [lbUser, setLbUser] = useState("");
  const [openTracklist, setOpenTracklist] = useState(null);
  const [customPlaylists, setCustomPlaylists] = useState([]);
  const [showImportModal, setShowImportModal] = useState(false);

  const [playlist, setPlaylist] = useState("weekly-exploration");
  const [dlmode, setDlmode] = useState("normal");
  const [noPersist, setNoPersist] = useState(false);
  const [excludeLocal, setExcludeLocal] = useState(false);

  const [running, setRunning] = useState(false);
  const [status, setStatus] = useState("");
  const [logEntries, setLogEntries] = useState([]);
  const [rawLog, setRawLog] = useState(false);
  const logRef = useRef(null);

  useEffect(() => {
    Promise.all([fetchConfig(), fetchCustomPlaylists().catch(() => [])]).then(
      ([{ values, sources }, customList]) => {
        setEnvSources(sources || {});
        setLbUser(values.LISTENBRAINZ_USER || "");
        setCustomPlaylists(customList);

        const s = {};
        for (const p of PLAYLISTS) {
          const cron = values[p.scheduleKey];
          s[p.value] = cron
            ? { enabled: true, editing: false, ...cronToFields(cron) }
            : {
                enabled: false,
                day: p.defaultDay,
                hour: p.defaultHour,
                minute: p.defaultMinute,
                editing: false,
              };
        }
        for (const cp of customList) {
          s[cp.id] = cp.schedule
            ? { enabled: true, editing: false, ...cronToFields(cp.schedule) }
            : cp.flags
              ? { enabled: true, editing: false, day: -2, hour: 4, minute: 0 }
              : { enabled: false, day: -1, hour: 4, minute: 0, editing: false };
        }
        setSchedules(s);
      },
    );
  }, []);

  const onLine = useCallback((data) => {
    setLogEntries((prev) => [...prev, { raw: data, ...parseSlogLine(data) }]);
    requestAnimationFrame(() => {
      if (logRef.current)
        logRef.current.scrollTop = logRef.current.scrollHeight;
    });
  }, []);

  const onDone = useCallback((code) => {
    setStatus(
      code === 0 ? "done ✓" : code === null ? "error" : `failed (exit ${code})`,
    );
    setRunning(false);
  }, []);

  const { connect, disconnect } = useSSE({ onLine, onDone });

  useEffect(() => {
    fetchRunStatus().then((s) => {
      if (s.running) {
        setRunning(true);
        setStatus("running…");
        setLogEntries([]);
        connect();
      }
    });
    return () => disconnect();
  }, [connect, disconnect]);

  const isScheduleLocked = (id) => {
    const p = PLAYLISTS.find((p) => p.value === id);
    return p ? envSources[p.scheduleKey] === "env" : false;
  };

  const nextRunText = (id) => {
    const s = schedules[id];
    if (!s?.enabled) return "Disabled";
    return SCHEDULE_DAYS.find((d) => d.value === s.day)?.summary || "Every day";
  };

  const flashStatus = (id, msg) => {
    setScheduleSaveStatus((prev) => ({ ...prev, [id]: msg }));
    if (msg === "Saved.")
      setTimeout(
        () => setScheduleSaveStatus((prev) => ({ ...prev, [id]: "" })),
        2000,
      );
  };

  const scheduleProps = (id) => {
    const s = schedules[id];
    return {
      schedule: s,
      scheduleSaveStatus: scheduleSaveStatus[id] || "",
      onToggle: (v) => {
        setSchedules((prev) => ({
          ...prev,
          [id]: { ...prev[id], enabled: v },
        }));
        saveSchedule(id, v, s.day, s.hour, s.minute)
          .then(() => flashStatus(id, "Saved."))
          .catch(() => flashStatus(id, "Error saving."));
      },
      onToggleEdit: () =>
        setSchedules((prev) => ({
          ...prev,
          [id]: { ...prev[id], editing: !prev[id].editing },
        })),
      onSave: () => {
        if (isScheduleLocked(id)) return;
        saveSchedule(id, s.enabled, s.day, s.hour, s.minute)
          .then(() => flashStatus(id, "Saved."))
          .catch(() => flashStatus(id, "Error saving."));
        setSchedules((prev) => ({
          ...prev,
          [id]: { ...prev[id], editing: false },
        }));
      },
      onCancelEdit: () =>
        setSchedules((prev) => ({
          ...prev,
          [id]: { ...prev[id], editing: false },
        })),
      onDayChange: (day) =>
        setSchedules((prev) => ({
          ...prev,
          [id]: { ...prev[id], day },
        })),
      onTimeChange: (val) => {
        const [h = "00", m = "00"] = val.split(":");
        setSchedules((prev) => ({
          ...prev,
          [id]: {
            ...prev[id],
            hour: parseInt(h) || 0,
            minute: parseInt(m) || 0,
          },
        }));
      },
    };
  };

  const handleRun = async () => {
    setRunning(true);
    setLogEntries([]);
    setStatus("running…");
    try {
      await startRun(playlist, dlmode, !noPersist, excludeLocal);
      connect();
    } catch (e) {
      if (e.conflict) {
        setStatus("already running");
        setRunning(false);
        return;
      }
      setStatus("error");
      setRunning(false);
    }
  };

  const handleStop = async () => {
    setStatus("stopping…");
    try {
      await stopRun();
    } catch {
      setStatus("error stopping run");
    }
  };

  if (!schedules) return null;

  return (
    <div>
      {/* Scheduled Playlists */}
      <div className="mt-6">
        <div className="text-[16px] font-bold tracking-tight text-white mb-3.5">
          Scheduled Playlists
        </div>
        <div className="grid grid-cols-1 min-[420px]:grid-cols-2 min-[720px]:grid-cols-4 gap-3 mt-3">
          {PLAYLISTS.map((p, i) => (
            <PlaylistCard
              key={p.value}
              playlist={p}
              {...scheduleProps(p.value)}
              locked={isScheduleLocked(p.value)}
              fixedSchedule={!!p.fixedSchedule}
              index={i}
              nextRunText={nextRunText(p.value)}
              tracklistOpen={openTracklist === p.value}
              onTracklistToggle={() =>
                setOpenTracklist((v) => (v === p.value ? null : p.value))
              }
            />
          ))}
        </div>
        <TracklistSlide
          show={
            openTracklist && PLAYLISTS.some((p) => p.value === openTracklist)
          }
          slideKey={openTracklist}
        >
          <TracklistDropdown lbUser={lbUser} playlist={openTracklist} />
        </TracklistSlide>
        <p className="text-[12px] text-muted mt-3">
          Schedule changes take effect after restarting the container.
        </p>
      </div>

      {/* Custom Playlists */}
      <CustomPlaylistsSection
        customPlaylists={customPlaylists}
        schedules={schedules}
        scheduleProps={scheduleProps}
        openTracklist={openTracklist}
        setOpenTracklist={setOpenTracklist}
        lbUser={lbUser}
        showImportModal={showImportModal}
        setShowImportModal={setShowImportModal}
        onImportedRefresh={() => {
          fetchCustomPlaylists()
            .then((list) => {
              setCustomPlaylists(list);
              setSchedules((prev) => {
                const next = { ...prev };
                for (const cp of list) {
                  if (next[cp.id]) continue;
                  next[cp.id] = cp.schedule
                    ? {
                        enabled: true,
                        editing: false,
                        ...cronToFields(cp.schedule),
                      }
                    : cp.flags
                      ? {
                          enabled: true,
                          editing: false,
                          day: -2,
                          hour: 4,
                          minute: 0,
                        }
                      : {
                          enabled: false,
                          day: -1,
                          hour: 4,
                          minute: 0,
                          editing: false,
                        };
                }
                return next;
              });
            })
            .catch(() => {});
          setShowImportModal(false);
        }}
        onSync={async (id) => {
          await startRun(id, "normal", true, false);
          setRunning(true);
          setStatus("running…");
          setLogEntries([]);
          connect();
        }}
        onDelete={async (id, opts) => {
          try {
            await deleteCustomPlaylist(id, opts);
            setCustomPlaylists((prev) => prev.filter((p) => p.id !== id));
            setSchedules((prev) => {
              const next = { ...prev };
              delete next[id];
              return next;
            });
            if (openTracklist === id) setOpenTracklist(null);
          } catch {}
        }}
      />

      {/* Manual Run */}
      <div className="mt-6">
        <div className="text-[16px] font-bold tracking-tight text-white mb-3.5">
          Manual Run
        </div>
        <div className="flex flex-col gap-1.5 mb-3">
          <label className="text-[12px] text-muted">Download mode</label>
          <div className="flex gap-1.5">
            {[
              {
                value: "normal",
                name: "Normal",
                desc: "Download only if the track isn't found locally",
              },
              {
                value: "skip",
                name: "Skip",
                desc: "No downloads — builds a playlist from tracks already in your library. Good for testing.",
              },
              {
                value: "force",
                name: "Force",
                desc: "Always download, ignoring local tracks",
              },
            ].map((m) => (
              <button
                key={m.value}
                onClick={() => setDlmode(m.value)}
                className={`px-3 py-1.5 text-[12px] rounded-[6px] border bg-surface cursor-pointer transition-colors
                  ${dlmode === m.value ? "border-accent text-accent" : "border-ui-border text-muted hover:border-[#404040] hover:text-white"}`}
              >
                {m.name}
              </button>
            ))}
          </div>
          <p className="text-[11px] text-muted">
            {
              {
                normal: "Download only if the track isn't found locally",
                skip: "No downloads — builds a playlist from tracks already in your library. Good for testing.",
                force: "Always download, ignoring local tracks",
              }[dlmode]
            }
          </p>
        </div>
        <div className="flex gap-2.5 items-center flex-wrap mb-2.5">
          <label className="text-[12px] text-muted">Playlist</label>
          <select
            className={selectCls}
            value={playlist}
            onChange={(e) => setPlaylist(e.target.value)}
          >
            {PLAYLISTS.map((p) => (
              <option key={p.value} value={p.value}>
                {p.name}
              </option>
            ))}
            {customPlaylists.length > 0 && <option disabled>---</option>}
            {customPlaylists.map((cp) => (
              <option key={cp.id} value={cp.id}>
                {cp.name}
              </option>
            ))}
          </select>
          <label
            className="flex items-center gap-1.5 text-[12px] text-muted cursor-pointer"
            title="When unchecked (default), previously generated playlists and their tracks are kept and added to over time. When checked, the playlist is wiped and rebuilt from scratch on each run."
          >
            <input
              type="checkbox"
              checked={noPersist}
              onChange={(e) => setNoPersist(e.target.checked)}
            />{" "}
            don't persist
          </label>
          <label
            className="flex items-center gap-1.5 text-[12px] text-muted cursor-pointer"
            title="When checked, tracks already found in your local library are excluded from the generated playlist."
          >
            <input
              type="checkbox"
              checked={excludeLocal}
              onChange={(e) => setExcludeLocal(e.target.checked)}
            />{" "}
            exclude local
          </label>
        </div>
        <div className="flex gap-2.5 items-center">
          <Button onClick={handleRun} disabled={running}>
            ▶ Run
          </Button>
          {running && (
            <button
              onClick={handleStop}
              className="bg-transparent border border-danger text-danger rounded-[6px] px-[18px] py-[7px] text-[13px] cursor-pointer hover:bg-danger hover:text-white transition-colors"
            >
              ■ Stop
            </button>
          )}
          <span className="text-[12px] text-muted">{status}</span>
        </div>
      </div>

      {/* Output */}
      <div className="mt-6">
        <div className="flex items-center justify-between mb-3.5">
          <SectionLabel className="">Output</SectionLabel>
          <label className="flex items-center gap-1.5 text-[12px] text-muted cursor-pointer">
            <input
              type="checkbox"
              checked={rawLog}
              onChange={(e) => setRawLog(e.target.checked)}
            />{" "}
            Raw
          </label>
        </div>
        <Panel ref={logRef} className="w-full h-[300px]">
          {logEntries.map((e, i) =>
            rawLog ? (
              <div
                key={i}
                className="font-mono text-[11px] text-accent whitespace-pre-wrap break-all py-px"
              >
                {e.raw}
              </div>
            ) : (
              <LogRow key={i} entry={e} />
            ),
          )}
        </Panel>
      </div>
    </div>
  );
}

// ── Download Path Tab ─────────────────────────────────────────────────────────
// Profile-picker for the PATH_TEMPLATE env key. Users select a preset folder
// structure (or author their own via the modal) and apply it. Pending changes
// are previewed inline before being written to .env via the confirm bar.

function DownloadPathSection() {
  const [profiles, setProfiles] = useState(() =>
    SEED_PRESETS.map((p) => ({ ...p, seed: true })),
  );
  const [appliedIdx, setAppliedIdx] = useState(null);
  const [selectedIdx, setSelectedIdx] = useState(null);
  const [loaded, setLoaded] = useState(false);
  const [saveStatus, setSaveStatus] = useState("");
  const [showModal, setShowModal] = useState(false);
  const [openMenuIdx, setOpenMenuIdx] = useState(null);
  const [enrichEnabled, setEnrichEnabled] = useState(false);
  const [templateEnabled, setTemplateEnabled] = useState(false);

  useEffect(() => {
    Promise.all([
      fetchConfig(),
      fetchPathTemplatePresets().catch(() => []),
    ]).then(([{ values }, jsonPresets]) => {
      const allProfiles = [
        ...SEED_PRESETS.map((p) => ({ ...p, seed: true })),
        ...jsonPresets,
      ];
      setEnrichEnabled(values.ENRICH_TRACK_METADATA === "true");
      const t = values.PATH_TEMPLATE || "";
      if (t) {
        setTemplateEnabled(true);
        const idx = allProfiles.findIndex((p) => p.template === t);
        if (idx >= 0) {
          setProfiles(allProfiles);
          setAppliedIdx(idx);
          setSelectedIdx(idx);
        } else {
          const customIdx = allProfiles.length;
          setProfiles([...allProfiles, { name: "Custom", template: t }]);
          setAppliedIdx(customIdx);
          setSelectedIdx(customIdx);
        }
      } else {
        setProfiles(allProfiles);
      }
      setLoaded(true);
    });
  }, []);

  const handleEnrichToggle = async () => {
    const next = !enrichEnabled;
    setEnrichEnabled(next);
    try {
      await saveEnrichMetadata(next);
    } catch {
      setEnrichEnabled(!next);
    }
  };

  const handleTemplateToggle = async () => {
    if (templateEnabled) {
      setTemplateEnabled(false);
      setAppliedIdx(null);
      setSelectedIdx(null);
      try {
        await savePathTemplate("");
      } catch {
        setTemplateEnabled(true);
      }
    } else {
      setTemplateEnabled(true);
      if (selectedIdx === null) setSelectedIdx(0);
    }
  };

  useEffect(() => {
    if (!showModal) return;
    const handle = (e) => {
      e.preventDefault();
      e.returnValue = "";
    };
    window.addEventListener("beforeunload", handle);
    return () => window.removeEventListener("beforeunload", handle);
  }, [showModal]);

  useEffect(() => {
    if (openMenuIdx === null) return;
    const handle = () => setOpenMenuIdx(null);
    document.addEventListener("click", handle);
    return () => document.removeEventListener("click", handle);
  }, [openMenuIdx]);

  const dirty = selectedIdx !== appliedIdx;

  if (!loaded) return null;

  const handleDeleteProfile = async (i) => {
    const profile = profiles[i];
    if (!profile.seed) {
      try {
        await deletePathTemplatePreset(profile.name);
      } catch {}
    }
    const newApplied =
      appliedIdx === i
        ? null
        : appliedIdx !== null && appliedIdx > i
          ? appliedIdx - 1
          : appliedIdx;
    const newSelected =
      selectedIdx === i
        ? newApplied
        : selectedIdx !== null && selectedIdx > i
          ? selectedIdx - 1
          : selectedIdx;
    setProfiles((prev) => prev.filter((_, j) => j !== i));
    setAppliedIdx(newApplied);
    setSelectedIdx(newSelected);
    setOpenMenuIdx(null);
  };
  const previewTemplate =
    selectedIdx !== null
      ? (profiles[selectedIdx]?.template ?? SEED_PRESETS[0].template)
      : SEED_PRESETS[0].template;

  const handleSave = async () => {
    const t = selectedIdx !== null ? profiles[selectedIdx].template : "";
    try {
      await savePathTemplate(t);
      setAppliedIdx(selectedIdx);
      setSaveStatus("Saved.");
      setTimeout(() => setSaveStatus(""), 2500);
    } catch {
      setSaveStatus("Error saving.");
    }
  };

  const handleSavePreset = async ({ name, template }) => {
    try {
      await addPathTemplatePreset(name, template);
    } catch {}
    const newIdx = profiles.length;
    setProfiles((prev) => [...prev, { name, template }]);
    setSelectedIdx(newIdx);
    setShowModal(false);
  };

  return (
    <div className="mt-6">
      <SectionLabel>Folder Structure</SectionLabel>
      {/* ENRICH_TRACK_METADATA toggle */}
      <div className="flex items-start justify-between mt-3 mb-1 gap-4">
        <div className="flex flex-col gap-0.5">
          <span className="text-[13px] text-white">Auto-tag songs</span>
          <span className="text-[11px] text-muted">
            Looks up track numbers, year, genre & more from MusicBrainz and
            writes them to downloaded files. Applies to scheduled playlists only
            — not custom imports.
          </span>
        </div>
        <button
          role="switch"
          aria-checked={enrichEnabled}
          onClick={handleEnrichToggle}
          className={`relative inline-flex h-[22px] w-10 shrink-0 cursor-pointer rounded-full transition-colors duration-200 ${enrichEnabled ? "bg-accent" : "bg-[#383838]"}`}
        >
          <span
            className={`inline-block h-[18px] w-[18px] my-[2px] rounded-full bg-white shadow transition-transform duration-200 ${enrichEnabled ? "translate-x-[20px]" : "translate-x-[2px]"}`}
          />
        </button>
      </div>

      {/* Organize into folders toggle */}
      <div className="flex items-start justify-between mt-3 mb-1 gap-4">
        <div className="flex flex-col gap-0.5">
          <span className="text-[13px] text-white">Organize into folders</span>
          <span className="text-[11px] text-muted">
            Sort downloads into subfolders by artist, album, etc.
          </span>
        </div>
        <button
          role="switch"
          aria-checked={templateEnabled}
          onClick={handleTemplateToggle}
          className={`relative inline-flex h-[22px] w-10 shrink-0 cursor-pointer rounded-full transition-colors duration-200 ${templateEnabled ? "bg-accent" : "bg-[#383838]"}`}
        >
          <span
            className={`inline-block h-[18px] w-[18px] my-[2px] rounded-full bg-white shadow transition-transform duration-200 ${templateEnabled ? "translate-x-[20px]" : "translate-x-[2px]"}`}
          />
        </button>
      </div>

      {templateEnabled && (
        <>
          {/* Current / pending path readout */}
          <div className="flex items-baseline gap-2.5 overflow-x-auto py-1 mt-6">
            <span
              className={`text-[11px] shrink-0 transition-colors ${dirty ? "text-accent" : "text-muted"}`}
            >
              {dirty ? "Preview:" : "Active:"}
            </span>
            <div className="text-[13px] font-medium whitespace-nowrap">
              <PathLine template={previewTemplate} />
            </div>
          </div>

          {/* Profile card grid */}
          <div className="grid grid-cols-1 min-[520px]:grid-cols-2 min-[720px]:grid-cols-4 gap-3 mt-2">
            {profiles.map((profile, i) => {
              const isSelected = i === selectedIdx;
              return (
                <div
                  key={i}
                  onClick={() => setSelectedIdx(i)}
                  className={`group relative flex flex-col gap-2.5 p-4 bg-transparent rounded-[8px] cursor-pointer select-none transition-all duration-[120ms] border
                ${
                  isSelected
                    ? "border-accent"
                    : "border-ui-border hover:border-[#404040] hover:-translate-y-px"
                }`}
                  style={{
                    minHeight: 112,
                    ...(isSelected
                      ? { boxShadow: "0 0 0 3px rgba(30,215,96,0.14)" }
                      : {}),
                  }}
                >
                  <div className="flex items-start justify-between gap-1.5">
                    <span className="text-[14px] font-medium text-white leading-snug">
                      {profile.name}
                    </span>
                    <div className="flex items-center gap-1.5 shrink-0">
                      {!profile.seed && (
                        <div
                          className="relative"
                          onClick={(e) => e.stopPropagation()}
                        >
                          <button
                            onClick={(e) => {
                              e.stopPropagation();
                              setOpenMenuIdx(openMenuIdx === i ? null : i);
                            }}
                            className="opacity-0 group-hover:opacity-100 transition-opacity bg-transparent border-none text-muted hover:text-white text-[15px] leading-none cursor-pointer px-1 py-0"
                            title="Options"
                          >
                            ···
                          </button>
                          {openMenuIdx === i && (
                            <div
                              className="absolute right-0 top-full mt-1 bg-well border border-ui-border rounded-[6px] z-10 py-1 min-w-[80px]"
                              style={{ boxShadow: "0 8px 24px #00000066" }}
                            >
                              <button
                                onClick={() => handleDeleteProfile(i)}
                                className="w-full text-left px-3 py-1.5 text-[12px] text-danger hover:bg-well transition-colors cursor-pointer bg-transparent border-none whitespace-nowrap"
                              >
                                Delete
                              </button>
                            </div>
                          )}
                        </div>
                      )}
                    </div>
                  </div>
                  <div className="mt-auto pt-2.5 border-t border-dashed border-ui-border">
                    <div className="text-[11px] leading-relaxed">
                      <PathLine template={profile.template} />
                    </div>
                  </div>
                </div>
              );
            })}

            {/* New template card */}
            <div
              onClick={() => setShowModal(true)}
              className="flex flex-col items-center justify-center gap-2 p-4 bg-transparent rounded-[8px] border border-dashed border-ui-border cursor-pointer transition-colors text-muted hover:text-accent hover:border-accent"
              style={{ minHeight: 112 }}
            >
              <span className="text-[26px] leading-none">+</span>
              <span className="text-[13px]">New template</span>
            </div>
          </div>

          {/* Confirm bar */}
          <AnimatePresence>
            {dirty && (
              <motion.div
                key="confirmbar"
                initial={{ opacity: 0, y: 6 }}
                animate={{ opacity: 1, y: 0 }}
                exit={{ opacity: 0, y: 6 }}
                transition={{ duration: 0.2 }}
                className="flex items-center gap-4 mt-5"
              >
                <span className="mr-auto text-[12px] text-muted">
                  {saveStatus || "Preview only — not applied yet."}
                </span>
                <button
                  onClick={() => setSelectedIdx(appliedIdx)}
                  className="bg-transparent border-none text-muted text-[13px] cursor-pointer p-0 hover:text-white transition-colors"
                >
                  Cancel
                </button>
                <Button onClick={handleSave}>Save folder structure</Button>
              </motion.div>
            )}
            {!dirty && saveStatus && (
              <motion.p
                key="savestatus"
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                className="text-[12px] text-accent mt-4"
              >
                {saveStatus}
              </motion.p>
            )}
          </AnimatePresence>
        </>
      )}

      <AnimatePresence>
        {showModal && (
          <PathTemplateModal
            onClose={() => setShowModal(false)}
            onSave={handleSavePreset}
            enrichEnabled={enrichEnabled}
          />
        )}
      </AnimatePresence>
    </div>
  );
}

// ── Config Tab ────────────────────────────────────────────────────────────────
// Raw .env file viewer/editor, plus wizard re-run and full reset actions.
// Fetches its own raw config text from the API.

function ConfigSection({ onWizard }) {
  const [rawConfig, setRawConfig] = useState("");
  const [editing, setEditing] = useState(false);
  const [saveStatus, setSaveStatus] = useState("");

  useEffect(() => {
    fetchConfigRaw().then((text) => setRawConfig(text));
  }, []);

  const handleSave = async () => {
    try {
      await saveConfig(rawConfig);
      setEditing(false);
      setSaveStatus("Saved.");
      setTimeout(() => setSaveStatus(""), 2500);
    } catch {
      setSaveStatus("Error saving.");
    }
  };

  const handleReset = async () => {
    if (
      !confirm(
        "Reset all settings? This will restart the container and take you back to setup.",
      )
    )
      return;
    try {
      await resetConfig();
      const poll = async () => {
        try {
          await fetch("/api/config");
          location.reload();
        } catch {
          setTimeout(poll, 1500);
        }
      };
      setTimeout(poll, 3000);
    } catch (e) {
      alert("Reset failed: " + e.message);
    }
  };

  return (
    <div>
      <div className="mt-6">
        <div className="flex items-center justify-between mb-3.5">
          <SectionLabel className="">Config file</SectionLabel>
          {!editing ? (
            <Button onClick={() => setEditing(true)}>Edit</Button>
          ) : (
            <div className="flex items-center gap-2.5">
              <span className="text-[12px] text-muted">{saveStatus}</span>
              <Button onClick={handleSave}>Save</Button>
              <Button
                onClick={() => {
                  fetchConfigRaw().then(setRawConfig);
                  setEditing(false);
                }}
              >
                Cancel
              </Button>
            </div>
          )}
        </div>

        {!editing ? (
          <pre
            className="bg-well border border-ui-border rounded-[6px] w-full h-[420px] overflow-y-auto p-3.5 font-mono text-[12px] leading-relaxed whitespace-pre break-normal"
            dangerouslySetInnerHTML={{ __html: highlightEnv(rawConfig) }}
          />
        ) : (
          <textarea
            className="bg-well border border-ui-border text-white rounded-[6px] w-full h-[420px] p-3.5 font-mono text-[12px] leading-relaxed resize-y outline-none focus:border-accent"
            value={rawConfig}
            onChange={(e) => setRawConfig(e.target.value)}
            spellCheck={false}
            autoComplete="off"
            autoCorrect="off"
            autoCapitalize="off"
          />
        )}
      </div>

      <div className="mt-6">
        <SectionLabel>Setup</SectionLabel>
        <div className="flex flex-col items-start gap-2.5">
          <button
            onClick={onWizard}
            className="bg-transparent border-none text-muted text-[13px] cursor-pointer p-0 hover:text-white transition-colors"
          >
            Re-run setup wizard →
          </button>
          <button
            onClick={handleReset}
            className="bg-transparent border-none text-[#c0392b] text-[13px] cursor-pointer p-0 hover:text-[#d65546] transition-colors"
          >
            Reset all settings
          </button>
        </div>
      </div>
    </div>
  );
}

// ── Logs Tab ──────────────────────────────────────────────────────────────────
// Displays the full server log file. Fetches its own log data from the API.

function LogsSection() {
  const [logFileEntries, setLogFileEntries] = useState([]);
  const panelRef = useRef(null);

  const loadLog = () => {
    fetchLogs().then((text) => {
      setLogFileEntries(
        text
          .split("\n")
          .filter((l) => l.trim())
          .map((l) => ({ raw: l, ...parseSlogLine(l) })),
      );
    });
  };

  useEffect(() => {
    loadLog();
  }, []);

  useEffect(() => {
    if (panelRef.current)
      panelRef.current.scrollTop = panelRef.current.scrollHeight;
  }, [logFileEntries]);

  return (
    <div className="mt-6">
      <div className="flex items-center justify-between mb-3.5">
        <SectionLabel className="">Log</SectionLabel>
        <button
          onClick={loadLog}
          className="bg-transparent border-none text-muted text-[11px] cursor-pointer p-0 hover:text-white transition-colors"
        >
          Refresh
        </button>
      </div>

      {logFileEntries.length === 0 ? (
        <p className="text-[12px] text-muted py-1">No log output yet.</p>
      ) : (
        <Panel ref={panelRef} className="h-[400px]">
          {logFileEntries.map((e, i) => (
            <LogRow key={i} entry={e} />
          ))}
        </Panel>
      )}
    </div>
  );
}

// ── Admin Tab ─────────────────────────────────────────────────────────────────
// Database table management for the structured config models.

const ADMIN_RESOURCE_UI = {
  servers: {
    summary: (row) =>
      `${row.Name || "Unnamed"} · ${row.Type || "unknown"} · ${row.URL || "no URL"}`,
  },
  downloaders: {
    summary: (row) =>
      `${row.Name || row.Type || "downloader"} · ${row.DownloadDir || "no download dir"}`,
  },
  "youtube-downloaders": {
    summary: (row) =>
      `${row.Name || "youtube"} · ${row.TrackExtension || "default ext"} · #${row.ID}`,
  },
  "slskd-downloaders": {
    summary: (row) =>
      `${row.Name || "slskd"} · ${row.URL || "no URL"} · #${row.ID}`,
  },
  "server-downloaders": {
    summary: (row) =>
      `server #${row.ServerID} · downloader #${row.DownloaderID} · priority ${row.Priority ?? 0}`,
  },
  users: {
    summary: (row) =>
      `${row.username || "user"} · ${row.role || "basic"} · ${row.lb_user_name || "no ListenBrainz user"}`,
  },
  playlists: {
    summary: (row) =>
      `${row.DisplayName || row.Name || "playlist"} · ${row.Kind || "default"} · ${row.Enabled ? "enabled" : "disabled"}`,
  },
  credentials: {
    summary: (row) => `${row.Name || row.Type || "credential"} · #${row.ID}`,
  },
  "user-server-credentials": {
    summary: (row) =>
      `user #${row.UserID} · server #${row.ServerID} · credential #${row.CredentialID}`,
  },
  "playlist-schedules": {
    summary: (row) =>
      `${row.Name || "schedule"} · ${row.Schedule || "no schedule"} · ${row.Flags || "no flags"}`,
  },
  "app-settings": {
    summary: (row) =>
      `${row.DownloadServices || "youtube"} · ${row.ListenBrainzDiscovery || "playlist"} · wizard ${row.WizardComplete ? "done" : "pending"}`,
  },
  "path-templates": {
    summary: (row) => `${row.Name || "template"} · ${row.Template || "empty"}`,
  },
};
function hydrateAdminResource(resource) {
  return {
    ...resource,
    summary:
      ADMIN_RESOURCE_UI[resource.key]?.summary ||
      ((row) => row.Name || row.name || `#${row[resource.idField]}`),
    template: resource.template || {},
    fields: resource.fields || [],
  };
}
const jsonBoxCls =
  "w-full min-h-[220px] bg-well border border-ui-border rounded-[6px] p-3 text-[12px] text-white font-mono outline-none focus:border-accent";

function safeJson(value) {
  return JSON.stringify(value ?? {}, null, 2);
}

function labelForFK(resource, id, refs) {
  if (id === null || id === undefined || id === "") return "None";
  const rows = refs[resource] || [];
  const row = rows.find((r) => String(r.id ?? r.ID) === String(id));
  if (!row) return `#${id}`;
  return row.username || row.Name || row.name || `#${id}`;
}

function AdminRowForm({ resource, value, refs, onChange, createMode = false }) {
  const [pathTemplateField, setPathTemplateField] = useState(null);
  const fields = resource.fields || [];
  const update = (key, next) => onChange({ ...(value || {}), [key]: next });
  return (
    <div className="grid md:grid-cols-2 gap-3">
      {fields
        .filter((f) => !f.createOnly || createMode)
        .map((field) => {
          const v = value?.[field.key];
          const baseCls =
            "w-full bg-well border border-ui-border rounded-[8px] px-3 py-2 text-[13px] text-white outline-none focus:border-accent";
          const control =
            field.type === "checkbox" ? (
              <button
                type="button"
                onClick={() => update(field.key, !v)}
                className={`flex items-center justify-between gap-3 bg-well border border-ui-border rounded-[8px] px-3 py-2 text-left transition-colors ${v ? "border-accent/60" : "hover:border-accent/60"}`}
              >
                <span className="text-[12px] text-muted">
                  {v ? "Enabled" : "Disabled"}
                </span>
                <span className="pointer-events-none">
                  <Toggle checked={!!v} onChange={() => {}} small />
                </span>
              </button>
            ) : field.type === "select" ? (
              <select
                className={baseCls}
                value={v ?? ""}
                onChange={(e) => update(field.key, e.target.value)}
              >
                {(field.options || []).map((opt) => (
                  <option key={opt} value={opt}>
                    {opt}
                  </option>
                ))}
              </select>
            ) : field.type === "fk" ? (
              <select
                className={baseCls}
                value={v ?? ""}
                onChange={(e) =>
                  update(
                    field.key,
                    e.target.value === "" ? null : Number(e.target.value),
                  )
                }
              >
                {field.nullable && <option value="">None</option>}
                {(refs[field.resource] || []).map((row) => {
                  const id = row.id ?? row.ID;
                  return (
                    <option key={id} value={id}>
                      {labelForFK(field.resource, id, refs)}
                    </option>
                  );
                })}
              </select>
            ) : field.type === "dir" ? (
              <DirInput
                value={v ?? ""}
                onChange={(next) => update(field.key, next)}
                placeholder="/data/"
              />
            ) : field.type === "path-template" ? (
              <div className="grid sm:grid-cols-[1fr_auto] gap-2">
                <input
                  className={baseCls}
                  value={v ?? ""}
                  onChange={(e) => update(field.key, e.target.value)}
                  placeholder="{{artist}}/{{album}}/{{title}}"
                />
                <Button
                  onClick={() => setPathTemplateField(field.key)}
                  className="px-3 py-2"
                >
                  Builder
                </Button>
              </div>
            ) : (
              <input
                className={baseCls}
                type={field.type || "text"}
                value={v ?? ""}
                onChange={(e) =>
                  update(
                    field.key,
                    field.type === "number"
                      ? e.target.value === "" && field.nullable
                        ? null
                        : Number(e.target.value)
                      : e.target.value,
                  )
                }
              />
            );
          return (
            <TextField key={field.key} label={field.label}>
              {control}
            </TextField>
          );
        })}
      <PathTemplateModal
        open={!!pathTemplateField}
        value={pathTemplateField ? value?.[pathTemplateField] || "" : ""}
        enrichEnabled={!!value?.EnrichTrackMetadata}
        onClose={() => setPathTemplateField(null)}
        onChange={(template) =>
          pathTemplateField && update(pathTemplateField, template)
        }
        onEnrichChange={(enabled) => {
          if (
            Object.prototype.hasOwnProperty.call(
              value || {},
              "EnrichTrackMetadata",
            )
          ) {
            update("EnrichTrackMetadata", enabled);
          }
        }}
      />
    </div>
  );
}

function PathTemplatesAdminPanel({
  rows,
  selectedId,
  selectRow,
  editorValue,
  setEditorValue,
  setEditorText,
  createValue,
  setCreateValue,
  setCreateText,
  saveRow,
  createRow,
  deleteRow,
}) {
  const updateEditor = (next) => {
    setEditorValue(next);
    setEditorText(safeJson(next));
  };
  const updateCreate = (next) => {
    setCreateValue(next);
    setCreateText(safeJson(next));
  };
  const appendToken = (target, token) => {
    const current =
      target === "editor"
        ? editorValue?.Template || ""
        : createValue?.Template || "";
    const nextTemplate = `${current}${current && !current.endsWith("/") ? "/" : ""}${token}`;
    if (target === "editor")
      updateEditor({ ...(editorValue || {}), Template: nextTemplate });
    else updateCreate({ ...(createValue || {}), Template: nextTemplate });
  };
  const builtIns = [
    { name: "Artist / Album", template: "{{artist}}/{{album}}/{{title}}" },
    { name: "Artist - Title", template: "{{artist}} - {{title}}" },
    { name: "Album / Track", template: "{{album}}/{{track}} - {{title}}" },
  ];
  return (
    <div className="grid lg:grid-cols-[300px_1fr] gap-4">
      <div className="grid gap-4">
        <Panel className="p-3 max-h-[340px] overflow-y-auto">
          <div className="text-[11px] uppercase tracking-[1px] text-muted mb-2">
            Database templates
          </div>
          {rows.length === 0 ? (
            <p className="text-[12px] text-muted">No saved DB templates yet.</p>
          ) : (
            rows.map((row) => (
              <button
                key={row.ID}
                onClick={() => selectRow(row)}
                className={`w-full text-left rounded-[8px] p-3 mb-2 border transition-colors ${String(selectedId) === String(row.ID) ? "border-accent bg-accent/10" : "border-ui-border bg-well hover:border-accent/60"}`}
              >
                <div className="text-[12px] text-white font-semibold">
                  {row.Name || `Template #${row.ID}`}
                </div>
                <div className="text-[11px] text-muted font-mono break-all mt-1">
                  {row.Template}
                </div>
              </button>
            ))
          )}
        </Panel>
        <Panel className="p-3">
          <div className="text-[11px] uppercase tracking-[1px] text-muted mb-2">
            Starter templates
          </div>
          {builtIns.map((item) => (
            <button
              key={item.name}
              onClick={() =>
                updateCreate({
                  ...(createValue || {}),
                  Name: item.name,
                  Template: item.template,
                })
              }
              className="w-full text-left rounded-[8px] p-2 mb-2 border border-transparent hover:border-ui-border hover:bg-white/5 transition-colors"
            >
              <div className="text-[12px] text-white">{item.name}</div>
              <div className="text-[11px] text-muted font-mono break-all">
                {item.template}
              </div>
            </button>
          ))}
        </Panel>
      </div>

      <div className="grid gap-4">
        <Panel className="p-4">
          <div className="flex items-center justify-between mb-3">
            <SectionLabel className="">Edit DB template</SectionLabel>
            <div className="flex gap-2">
              <Button
                onClick={saveRow}
                disabled={!selectedId}
                className="px-3 py-1.5"
              >
                Save
              </Button>
              <Button
                onClick={deleteRow}
                disabled={!selectedId}
                className="px-3 py-1.5 border-danger text-danger hover:border-danger"
              >
                Delete
              </Button>
            </div>
          </div>
          {selectedId ? (
            <div className="grid gap-3">
              <TextField label="Name">
                <input
                  className="w-full bg-well border border-ui-border rounded-[8px] px-3 py-2 text-[13px] text-white outline-none focus:border-accent"
                  value={editorValue?.Name || ""}
                  onChange={(e) =>
                    updateEditor({
                      ...(editorValue || {}),
                      Name: e.target.value,
                    })
                  }
                />
              </TextField>
              <TextField label="Template">
                <input
                  className="w-full bg-well border border-ui-border rounded-[8px] px-3 py-2 text-[13px] text-white font-mono outline-none focus:border-accent"
                  value={editorValue?.Template || ""}
                  onChange={(e) =>
                    updateEditor({
                      ...(editorValue || {}),
                      Template: e.target.value,
                    })
                  }
                  placeholder="{{artist}}/{{album}}/{{title}}"
                />
              </TextField>
              <div className="flex flex-wrap gap-2">
                {PATH_TEMPLATE_TOKENS.map((token) => (
                  <button
                    key={token}
                    onClick={() => appendToken("editor", token)}
                    className="rounded-full border border-ui-border px-3 py-1.5 text-[12px] text-muted hover:text-white hover:border-accent transition-colors"
                  >
                    {token}
                  </button>
                ))}
              </div>
              <p className="text-[11px] text-muted">
                These presets are stored in the database and appear in the
                wizard/template builder.
              </p>
            </div>
          ) : (
            <div className="text-[12px] text-muted border border-ui-border rounded-[8px] p-4">
              Select a DB template to edit.
            </div>
          )}
        </Panel>

        <Panel className="p-4">
          <div className="flex items-center justify-between mb-3">
            <SectionLabel className="">Create DB template</SectionLabel>
            <Button onClick={createRow} className="px-3 py-1.5">
              Create
            </Button>
          </div>
          <div className="grid gap-3">
            <TextField label="Name">
              <input
                className="w-full bg-well border border-ui-border rounded-[8px] px-3 py-2 text-[13px] text-white outline-none focus:border-accent"
                value={createValue?.Name || ""}
                onChange={(e) =>
                  updateCreate({ ...(createValue || {}), Name: e.target.value })
                }
                placeholder="My folder layout"
              />
            </TextField>
            <TextField label="Template">
              <input
                className="w-full bg-well border border-ui-border rounded-[8px] px-3 py-2 text-[13px] text-white font-mono outline-none focus:border-accent"
                value={createValue?.Template || ""}
                onChange={(e) =>
                  updateCreate({
                    ...(createValue || {}),
                    Template: e.target.value,
                  })
                }
                placeholder="{{artist}}/{{album}}/{{title}}"
              />
            </TextField>
            <div className="flex flex-wrap gap-2">
              {PATH_TEMPLATE_TOKENS.map((token) => (
                <button
                  key={token}
                  onClick={() => appendToken("create", token)}
                  className="rounded-full border border-ui-border px-3 py-1.5 text-[12px] text-muted hover:text-white hover:border-accent transition-colors"
                >
                  {token}
                </button>
              ))}
            </div>
          </div>
        </Panel>
      </div>
    </div>
  );
}

function AdminSection() {
  const [activeResource, setActiveResource] = useState(ADMIN_RESOURCES[0].key);
  const [rows, setRows] = useState([]);
  const [selectedId, setSelectedId] = useState(null);
  const [editorText, setEditorText] = useState("");
  const [createText, setCreateText] = useState(
    safeJson(ADMIN_RESOURCES[0].template),
  );
  const [status, setStatus] = useState("");
  const [error, setError] = useState("");
  const [refs, setRefs] = useState({});
  const [editorValue, setEditorValue] = useState(null);
  const [createValue, setCreateValue] = useState(ADMIN_RESOURCES[0].template);
  const resource =
    ADMIN_RESOURCES.find((r) => r.key === activeResource) ?? ADMIN_RESOURCES[0];

  const loadRows = useCallback(async () => {
    setError("");
    setStatus("Loading…");
    try {
      const data = await fetchAdminResource(resource.key);
      setRows(data);
      setStatus(`Loaded ${data.length} ${resource.label.toLowerCase()}.`);
      const selected =
        data.find(
          (row) => String(row[resource.idField]) === String(selectedId),
        ) ?? data[0];
      setSelectedId(selected ? selected[resource.idField] : null);
      setEditorText(selected ? safeJson(selected) : "");
      setEditorValue(selected || null);
    } catch (err) {
      setError(err.message || String(err));
      setStatus("");
    }
  }, [resource, selectedId]);

  useEffect(() => {
    setSelectedId(null);
    setEditorText("");
    setCreateText(safeJson(resource.template));
    setCreateValue(resource.template);
  }, [resource.key]);

  useEffect(() => {
    loadRows();
  }, [loadRows]);

  useEffect(() => {
    Promise.all(
      [
        "users",
        "servers",
        "credentials",
        "downloaders",
        "youtube-downloaders",
        "slskd-downloaders",
      ].map((key) =>
        fetchAdminResource(key)
          .then((rows) => [key, rows])
          .catch(() => [key, []]),
      ),
    ).then((entries) => setRefs(Object.fromEntries(entries)));
  }, []);

  const selectRow = (row) => {
    setSelectedId(row[resource.idField]);
    setEditorText(safeJson(row));
    setEditorValue(row);
    setError("");
    setStatus("");
  };

  const parseEditor = (text) => {
    try {
      return JSON.parse(text || "{}");
    } catch (err) {
      throw new Error(`Invalid JSON: ${err.message}`);
    }
  };

  const createRow = async () => {
    setError("");
    try {
      const created = await createAdminResource(
        resource.key,
        createValue || parseEditor(createText),
      );
      setStatus("Created row.");
      setCreateText(safeJson(resource.template));
      setCreateValue(resource.template);
      setSelectedId(created[resource.idField]);
      await loadRows();
    } catch (err) {
      setError(err.message || String(err));
    }
  };

  const saveRow = async () => {
    if (!selectedId) return;
    setError("");
    try {
      const body = editorValue || parseEditor(editorText);
      await updateAdminResource(resource.key, selectedId, body);
      setStatus("Saved row.");
      await loadRows();
    } catch (err) {
      setError(err.message || String(err));
    }
  };

  const deleteRow = async () => {
    if (!selectedId) return;
    if (!confirm(`Delete ${resource.label} row ${selectedId}?`)) return;
    setError("");
    try {
      await deleteAdminResource(resource.key, selectedId);
      setSelectedId(null);
      setEditorText("");
      setStatus("Deleted row.");
      await loadRows();
    } catch (err) {
      setError(err.message || String(err));
    }
  };

  return (
    <div className="mt-6 grid gap-5">
      <div className="bg-panel border border-ui-border rounded-[10px] p-5 shadow-card">
        <div className="flex items-center justify-between gap-3 mb-4">
          <div>
            <SectionLabel className="mb-1">Admin Management</SectionLabel>
            <p className="text-[12px] text-muted">
              Create, read, update, and delete structured database records.
            </p>
          </div>
          <Button onClick={loadRows} className="px-3 py-1.5">
            Refresh
          </Button>
        </div>

        <div className="flex flex-wrap gap-2 mb-4">
          {ADMIN_RESOURCES.map((r) => (
            <button
              key={r.key}
              onClick={() => setActiveResource(r.key)}
              className={`rounded-full border px-3 py-1.5 text-[12px] transition-colors ${r.key === resource.key ? "border-accent text-accent bg-accent/10" : "border-ui-border text-muted hover:text-white hover:border-accent"}`}
            >
              {r.label}
            </button>
          ))}
        </div>

        {error && (
          <div className="mb-3 text-[12px] text-danger whitespace-pre-wrap">
            {error}
          </div>
        )}
        {status && !error && (
          <div className="mb-3 text-[12px] text-muted">{status}</div>
        )}

        {resource.key === "path-templates" ? (
          <PathTemplatesAdminPanel
            rows={rows}
            selectedId={selectedId}
            selectRow={selectRow}
            editorValue={editorValue}
            setEditorValue={setEditorValue}
            setEditorText={setEditorText}
            createValue={createValue}
            setCreateValue={setCreateValue}
            setCreateText={setCreateText}
            saveRow={saveRow}
            createRow={createRow}
            deleteRow={deleteRow}
          />
        ) : (
          <div className="grid lg:grid-cols-[280px_1fr] gap-4">
            <Panel className="max-h-[520px] p-0">
              {rows.length === 0 ? (
                <p className="text-[12px] text-muted p-3">No rows yet.</p>
              ) : (
                rows.map((row) => {
                  const id = row[resource.idField];
                  return (
                    <button
                      key={id}
                      onClick={() => selectRow(row)}
                      className={`w-full text-left bg-transparent border-0 border-b border-ui-border p-3 cursor-pointer hover:bg-white/5 ${String(selectedId) === String(id) ? "bg-accent/10" : ""}`}
                    >
                      <div className="text-[12px] text-white font-medium">
                        #{id} {resource.summary(row)}
                      </div>
                      <div className="text-[11px] text-muted mt-1">
                        {resource.key}
                      </div>
                    </button>
                  );
                })
              )}
            </Panel>

            <div className="grid gap-4">
              <div>
                <div className="flex items-center justify-between mb-2">
                  <SectionLabel className="">Edit selected row</SectionLabel>
                  <div className="flex gap-2">
                    <Button
                      onClick={saveRow}
                      disabled={!selectedId}
                      className="px-3 py-1.5"
                    >
                      Save
                    </Button>
                    <Button
                      onClick={deleteRow}
                      disabled={!selectedId}
                      className="px-3 py-1.5 border-danger text-danger hover:border-danger"
                    >
                      Delete
                    </Button>
                  </div>
                </div>
                {selectedId ? (
                  <AdminRowForm
                    resource={resource}
                    value={editorValue}
                    refs={refs}
                    onChange={(v) => {
                      setEditorValue(v);
                      setEditorText(safeJson(v));
                    }}
                  />
                ) : (
                  <div className="text-[12px] text-muted border border-ui-border rounded-[8px] p-4">
                    Select a row to edit.
                  </div>
                )}
                <details className="mt-3">
                  <summary className="text-[11px] text-muted cursor-pointer">
                    Advanced JSON
                  </summary>
                  <textarea
                    className={jsonBoxCls + " mt-2"}
                    value={editorText}
                    disabled={!selectedId}
                    onChange={(e) => {
                      setEditorText(e.target.value);
                      try {
                        setEditorValue(JSON.parse(e.target.value || "{}"));
                      } catch {}
                    }}
                    spellCheck={false}
                  />
                </details>
                {resource.key === "users" && (
                  <p className="text-[11px] text-muted mt-2">
                    User passwords are write-only. Add a password field when
                    creating or updating to set a new password.
                  </p>
                )}
              </div>

              <div>
                <div className="flex items-center justify-between mb-2">
                  <SectionLabel className="">Create new row</SectionLabel>
                  <Button onClick={createRow} className="px-3 py-1.5">
                    Create
                  </Button>
                </div>
                <AdminRowForm
                  resource={resource}
                  value={createValue}
                  refs={refs}
                  createMode
                  onChange={(v) => {
                    setCreateValue(v);
                    setCreateText(safeJson(v));
                  }}
                />
                <details className="mt-3">
                  <summary className="text-[11px] text-muted cursor-pointer">
                    Advanced JSON
                  </summary>
                  <textarea
                    className={jsonBoxCls + " mt-2"}
                    value={createText}
                    onChange={(e) => {
                      setCreateText(e.target.value);
                      try {
                        setCreateValue(JSON.parse(e.target.value || "{}"));
                      } catch {}
                    }}
                    spellCheck={false}
                  />
                </details>
              </div>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

// ── Settings ──────────────────────────────────────────────────────────────────
// Tab shell. Routes between Home, Settings, and Logs sections.

// Module-level cache so the picked cover survives component remounts.
let _bgCoverCache = null;

export default function Settings({ onWizard, onLogout }) {
  const [activeTab, setActiveTab] = useState("run");
  const [bgCover, setBgCover] = useState(_bgCoverCache);

  useEffect(() => {
    if (_bgCoverCache) return;
    Promise.all(
      ["weekly-exploration", "weekly-jams", "daily-jams", "on-repeat"].map(
        (t) => fetchPlaylistTracks(t).catch(() => ({ tracks: [] })),
      ),
    ).then((results) => {
      const covers = results.flatMap((r) =>
        (r.tracks ?? []).map((t) => t.coverUrl).filter(Boolean),
      );
      if (covers.length) {
        _bgCoverCache = covers[Math.floor(Math.random() * covers.length)];
        setBgCover(_bgCoverCache);
      }
    });
  }, []);

  return (
    <div style={{ position: "relative", minHeight: "100vh" }}>
      <UpdateNotification />
      {/* Page background art */}
      <div
        style={{
          position: "fixed",
          inset: 0,
          zIndex: 0,
          background: "#121212",
          overflow: "hidden",
          willChange: "transform",
        }}
      >
        <AnimatePresence>
          {bgCover && (
            <motion.img
              key={bgCover}
              src={bgCover}
              alt=""
              initial={{ opacity: 0 }}
              animate={{ opacity: 1 }}
              exit={{ opacity: 0 }}
              transition={{ duration: 2, ease: "easeInOut" }}
              style={{
                position: "absolute",
                inset: 0,
                width: "100%",
                height: "100%",
                objectFit: "cover",
                filter: "blur(90px) saturate(1.8) brightness(0.14)",
                transform: "scale(1.15) translateZ(0)",
                willChange: "opacity",
              }}
            />
          )}
        </AnimatePresence>
      </div>

      {/* Content */}
      <div style={{ position: "relative", zIndex: 1 }} className="min-h-screen">
        <div className="max-w-[980px] mx-auto px-6 pb-12">
          <header className="flex items-baseline gap-4 pt-5 pb-0 border-b border-ui-border mb-6">
            <span className="text-[16px] leading-none font-bold tracking-tight text-accent">
              Explo
            </span>
            <nav className="flex gap-0.5 items-baseline flex-1">
              <button
                className={tabBtnCls(activeTab === "run")}
                onClick={() => setActiveTab("run")}
              >
                Home
              </button>
              <button
                className={tabBtnCls(activeTab === "config")}
                onClick={() => setActiveTab("config")}
              >
                Settings
              </button>
              <button
                className={tabBtnCls(activeTab === "admin")}
                onClick={() => setActiveTab("admin")}
              >
                Admin
              </button>
              <button
                className={tabBtnCls(activeTab === "logs")}
                onClick={() => setActiveTab("logs")}
              >
                Logs
              </button>
            </nav>
            <button
              onClick={onLogout}
              className="pb-2 text-[12px] text-muted hover:text-white transition-colors cursor-pointer bg-transparent border-none"
            >
              Sign out
            </button>
          </header>

          {activeTab === "run" && <HomeSection />}
          {activeTab === "config" && <ConfigSection onWizard={onWizard} />}
          {activeTab === "admin" && <AdminSection />}
          {activeTab === "logs" && <LogsSection />}
        </div>
      </div>
    </div>
  );
}
