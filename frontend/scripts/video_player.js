const MAIN_SERVER = 'http://localhost:8080';
const videoElement = document.getElementById('videoPlayer');
const urlParams = new URLSearchParams(window.location.search);
const sessionKey = urlParams.get('sessionKey');
const isHost = urlParams.has('host');
let ws = null;

async function initializeSession() {
    try {
        const response = await fetch(`${MAIN_SERVER}/api/sessions/${encodeURIComponent(sessionKey)}`);
        const sessionData = await response.json();
        
        document.getElementById('currentServer').textContent = new URL(sessionData.streaming_url).hostname;
        document.getElementById('videoSource').src = sessionData.streaming_url; // Directly use the URL
        videoElement.load(); 
        
        connectWebSocket(sessionData.streaming_url);

    } catch (error) {
        setStatus('Failed to initialize session: ' + error.message, true);
    }
}

function connectWebSocket() {
    const hostToken = sessionStorage.getItem("hostToken");
    const wsUrl = new URL(`${MAIN_SERVER.replace('http', 'ws')}/ws`);
    wsUrl.searchParams.set('sessionKey', sessionKey);
    if (hostToken) {
        wsUrl.searchParams.set('hostToken', hostToken); // Include hostToken if present
    }
    ws = new WebSocket(wsUrl);

    ws.onopen = () => {
        setStatus('Connected to session', false);
        document.getElementById('sessionKeyDisplay').textContent = sessionKey;
    };

    ws.onmessage = (event) => {
        const data = JSON.parse(event.data);
        switch(data.type) {
            case 'init':
                handleInitialization(data);
                break;
            case 'state_update':
                handleStateUpdate(data);
                break;
            case 'server_change':
                handleServerMigration(data.newUrl);
                break;
        }
    };

    ws.onclose = () => {
        setStatus('Connection lost - reconnecting...', true);
        setTimeout(connectWebSocket, 3000);
    };
}

function handleInitialization(data) {
    if (data.isHost) {
        document.getElementById('userRole').textContent = 'Host';
        document.getElementById('hostControls').style.display = 'block';
        document.querySelectorAll('.host-control').forEach(btn => btn.disabled = false);
    }
}

function handleStateUpdate(data) {
    const serverTime = data.timestamp;
    const latency = Date.now() - serverTime;
    document.getElementById('serverLatency').textContent = `${latency}ms`;

    if (Math.abs(videoElement.currentTime - data.state.currentTime) > 0.5) {
        videoElement.currentTime = data.state.currentTime + (latency / 2000);
    }

    if (data.state.paused !== videoElement.paused) {
        data.state.paused ? videoElement.pause() : videoElement.play();
    }
}

function handleServerMigration(newUrl) {
    const currentTime = videoElement.currentTime;
    document.getElementById('videoSource').src = `${newUrl}/video?t=${currentTime}`;
    document.getElementById('currentServer').textContent = new URL(newUrl).hostname;
    videoElement.play();
}

document.getElementById('forceSync').addEventListener('click', () => {
    ws.send(JSON.stringify({
        type: 'force_sync',
        currentTime: videoElement.currentTime
    }));
});

document.getElementById('migrateServer').addEventListener('click', async () => {
    try {
        const response = await fetch(`${MAIN_SERVER}/api/sessions/${sessionKey}/migrate`, {
            method: 'POST'
        });
        const { newUrl } = await response.json();
        handleServerMigration(newUrl);
    } catch (error) {
        setStatus('Migration failed: ' + error.message, true);
    }
});

videoElement.addEventListener('timeupdate', () => {
    document.getElementById('currentTime').textContent = 
        new Date(videoElement.currentTime * 1000).toISOString().substr(11, 8);
    document.getElementById('seekBar').value = videoElement.currentTime;
});

function setStatus(message, isError) {
    const statusElement = document.getElementById('statusMessage');
    statusElement.textContent = message;
    statusElement.style.color = isError ? '#dc3545' : '#28a745';
    statusElement.style.display = 'block';
    setTimeout(() => statusElement.style.display = 'none', 3000);
}



// Initialize
initializeSession();