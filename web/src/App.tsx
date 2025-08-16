import { useEffect, useState } from "react";
import "./App.css";
import axios from "axios";
import {
  AppBar,
  Toolbar,
  Typography,
  Box,
  Chip,
  Container,
  Paper,
  Stack,
  Button,
  TextField,
  CssBaseline,
  ThemeProvider,
  createTheme,
} from "@mui/material";
import CloudUploadIcon from "@mui/icons-material/CloudUpload";

type Variants = Record<string, Record<string, string>>;

type MetricsPayload = {
  total_uploads: number;
  total_variants: number;
  failed_variants: number;
  worker_active: number;
  worker_started: number;
  per_op?: {
    active?: Record<string, number>;
    success?: Record<string, number>;
    failed?: Record<string, number>;
  };
};

const theme = createTheme({
  palette: {
    mode: "light",
    background: { default: "#f7f7f9" },
  },
  shape: { borderRadius: 10 },
});

function App() {
  const [variants, setVariants] = useState<Variants>({});
  const [uploading, setUploading] = useState(false);
  const [m, setM] = useState<MetricsPayload>({
    total_uploads: 0,
    total_variants: 0,
    failed_variants: 0,
    worker_active: 0,
    worker_started: 0,
    per_op: { active: {}, success: {}, failed: {} },
  });

  const refresh = async () => {
    const { data } = await axios.get<Variants>("/images");
    setVariants(data || {});
    try {
      const { data: met } = await axios.get<MetricsPayload>("/metrics/json");
      setM((prev) => ({ ...prev, ...(met || {}) }));
    } catch {
      try {
        const { data: met2 } = await axios.get<MetricsPayload>(
          "http://localhost:8080/metrics/json"
        );
        setM((prev) => ({ ...prev, ...(met2 || {}) }));
      } catch {}
    }
  };

  // SSE live updates
  useEffect(() => {
    const es = new EventSource("/events");
    es.onmessage = (ev) => {
      try {
        const payload = JSON.parse(ev.data);
        if (payload.variants) setVariants(payload.variants as Variants);
        if (payload.metrics)
          setM((prev) => ({ ...prev, ...(payload.metrics as MetricsPayload) }));
      } catch {}
    };
    es.onerror = () => es.close();
    return () => es.close();
  }, []);

  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 10000);
    return () => clearInterval(t);
  }, []);

  const onUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    if (!e.target.files?.[0]) return;
    setUploading(true);
    const form = new FormData();
    form.append("file", e.target.files[0]);
    try {
      await axios.post("/upload", form);
      await refresh();
    } finally {
      setUploading(false);
      e.target.value = "";
    }
  };

  const ops = ["thumbnail", "grayscale", "blur", "rotate90"] as const;

  return (
    <ThemeProvider theme={theme}>
      <CssBaseline />

      <AppBar position="sticky" elevation={0} color="inherit">
        <Toolbar>
          <Typography variant="h6" sx={{ flex: 1 }}>
            Distributed Image Factory
          </Typography>
          <Stack direction="row" spacing={1}>
            <Chip
              label={`uploads ${m.total_uploads}`}
              color="primary"
              variant="outlined"
            />
            <Chip
              label={`variants ${m.total_variants}`}
              color="success"
              variant="outlined"
            />
            <Chip
              label={`failed ${m.failed_variants}`}
              color="error"
              variant="outlined"
            />
            <Chip
              label={`workers ${m.worker_active}`}
              color="info"
              variant="outlined"
            />
          </Stack>
        </Toolbar>
      </AppBar>

      <Container sx={{ mt: 2, mb: 4 }}>
        <Stack spacing={2}>
          {/* Controls row */}
          <Stack direction={{ xs: "column", md: "row" }} spacing={2}>
            <Paper sx={{ flex: 1, p: 2 }}>
              <Typography variant="subtitle1" sx={{ mb: 1 }}>
                Upload
              </Typography>
              <Box
                onDragOver={(e) => e.preventDefault()}
                onDrop={async (e) => {
                  e.preventDefault();
                  if (!e.dataTransfer.files?.[0]) return;
                  const fakeEvt: any = {
                    target: { files: e.dataTransfer.files },
                  };
                  await onUpload(fakeEvt);
                }}
                sx={{
                  border: "2px dashed #c6c6c6",
                  p: 2,
                  borderRadius: 2,
                  textAlign: "center",
                }}
              >
                Drag & drop an image here
                <Box sx={{ mt: 1 }}>
                  <Button
                    variant="contained"
                    startIcon={<CloudUploadIcon />}
                    component="label"
                    size="small"
                  >
                    Choose file
                    <input
                      hidden
                      type="file"
                      accept="image/*"
                      onChange={onUpload}
                      disabled={uploading}
                    />
                  </Button>
                  {uploading && (
                    <Typography variant="caption" sx={{ ml: 1 }}>
                      Uploading...
                    </Typography>
                  )}
                </Box>
              </Box>
            </Paper>

            <Paper sx={{ flex: 1, p: 2 }}>
              <Typography variant="subtitle1" sx={{ mb: 1 }}>
                Workers
              </Typography>
              <Stack spacing={1.2}>
                {ops.map((op) => (
                  <WorkerRow
                    key={op}
                    op={op}
                    active={m.per_op?.active?.[op] || 0}
                    success={m.per_op?.success?.[op] || 0}
                    fail={m.per_op?.failed?.[op] || 0}
                  />
                ))}
              </Stack>
            </Paper>
          </Stack>

          {/* Gallery */}
          <Box
            sx={{
              display: "grid",
              gridTemplateColumns: "repeat(auto-fill, minmax(320px, 1fr))",
              gap: 2,
            }}
          >
            {Object.entries(variants).map(([id, opsMap]) => (
              <Paper key={id} sx={{ p: 2 }}>
                <Typography sx={{ fontWeight: 700, mb: 1 }}>{id}</Typography>
                <Stack spacing={1}>
                  {Object.entries(opsMap).map(([op, url]) => (
                    <Box key={op}>
                      <Typography variant="caption" sx={{ color: "#666" }}>
                        {op}
                      </Typography>
                      <Box
                        component="img"
                        src={url}
                        sx={{
                          width: "100%",
                          height: 200,
                          objectFit: "cover",
                          borderRadius: 1,
                          border: "1px solid #e5e5e5",
                        }}
                      />
                    </Box>
                  ))}
                </Stack>
              </Paper>
            ))}
          </Box>
        </Stack>
      </Container>
    </ThemeProvider>
  );
}

export default App;

function WorkerRow({
  op,
  active,
  success,
  fail,
}: {
  op: string;
  active: number;
  success: number;
  fail: number;
}) {
  const [n, setN] = useState(1);
  const [busy, setBusy] = useState(false);
  const total = success + fail || 1;
  const okPct = Math.round((success / total) * 100);
  const failPct = 100 - okPct;
  return (
    <Stack
      direction={{ xs: "column", sm: "row" }}
      alignItems="center"
      spacing={1.5}
      sx={{
        p: 1,
        bgcolor: "#fafafa",
        border: "1px solid #eee",
        borderRadius: 2,
      }}
    >
      <Typography variant="body2" sx={{ width: 100 }}>
        {op}
      </Typography>
      <TextField
        size="small"
        type="number"
        inputProps={{ min: 1 }}
        value={n}
        onChange={(e) => setN(parseInt(e.target.value || "1", 10))}
        sx={{ width: 90 }}
      />
      <Button
        size="small"
        variant="contained"
        disabled={busy}
        onClick={async () => {
          setBusy(true);
          try {
            await axios.post("/admin/scale", { op, n });
          } finally {
            setBusy(false);
          }
        }}
      >
        start
      </Button>
      <Chip size="small" label={`active ${active}`} />
      <Box
        sx={{
          flex: 1,
          height: 10,
          bgcolor: "#eee",
          borderRadius: 1,
          overflow: "hidden",
          display: "flex",
        }}
      >
        <Box sx={{ width: `${okPct}%`, bgcolor: "#4caf50" }} />
        <Box sx={{ width: `${failPct}%`, bgcolor: "#ef5350" }} />
      </Box>
    </Stack>
  );
}
