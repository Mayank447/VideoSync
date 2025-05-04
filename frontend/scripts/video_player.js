const BACKEND_URL = 'http://localhost:8080';
const STREAMING_URL = ""
let isHost = false;
// let ws = null;
let latency = 0;

const videoElement = document.getElementById('videoPlayer');
const urlParams = new URLSearchParams(window.location.search);
const sessionKey = urlParams.get('sessionKey');

// Initialize WebSocket connection
function connectWebSocket(streamingUrl) {
    const wsUrl = new URL(`${streamingUrl.replace('http', 'ws')}/ws`);
    wsUrl.searchParams.set('sessionID', sessionKey);
    if (isHost) {
        wsUrl.searchParams.set('isHost', 'true');
    }

    ws = new WebSocket(wsUrl);

    ws.onopen = () => {
        console.log('WebSocket connected');
        setStatus('Connected to session', false);
    };

    ws.onmessage = (event) => {
        const data = JSON.parse(event.data);
        handleSyncMessage(data);
    };

    ws.onclose = () => {
        console.log('WebSocket disconnected');
        setStatus('Connection lost - attempting to reconnect...', true);
        setTimeout(() => connectWebSocket(streamingUrl), 3000);
    };

    ws.onerror = (error) => {
        console.error('WebSocket error:', error);
        setStatus('Connection error', true);
    };
}

// Handle synchronization messages
function handleSyncMessage(data) {
    switch (data.type) {
        case 'init':
            handleInitialization(data);
            break;
        case 'stateUpdate':
            handleStateUpdate(data);
            break;
        case 'heartbeat':
            handleHeartbeat(data);
            break;
    }
}

function handleInitialization(data) {
    isHost = data.isHost;
    document.getElementById('userRole').textContent = isHost ? 'Admin' : 'Participant';
    updateControls();
    attachCustomControlListeners(); // <- Attach buttons only once we know isHost

    videoElement.currentTime = data.state.currentTime;
    videoElement.playbackRate = data.state.playbackRate;
    data.state.paused ? videoElement.pause() : videoElement.play();
}

function handleStateUpdate(data) {
    const serverTime = data.servertime;
    const localTime = Date.now();

    latency = localTime - serverTime;

    const currentTime = videoElement.currentTime;
    const targetTime = data.state.currentTime + (latency / 1000);

    if (Math.abs(currentTime - targetTime) > 0.5) {
        videoElement.currentTime = targetTime;
    }

    if (data.state.paused !== videoElement.paused) {
        data.state.paused ? videoElement.pause() : videoElement.play();
    }

    videoElement.playbackRate = data.state.playbackRate;
}

function handleHeartbeat() {
    ws.send(JSON.stringify({ type: 'heartbeatAck' }));
}

// Update UI controls
function updateControls() {
    const playPauseBtn = document.getElementById('playPauseBtn');
    const seekBar = document.getElementById('seekBar');
    playPauseBtn.disabled = !isHost;
    seekBar.disabled = !isHost;
}

// Create new session
async function createNewSession() {
    try {
        console.log('Creating new session...');
        const response = await fetch(`${BACKEND_URL}/api/sessions`, {
            method: 'POST'
        });

        if (!response.ok) {
            throw new Error(`HTTP error! status: ${response.status}`);
        }

        const data = await response.json();
        console.log('Session created:', data);
        sessionStorage.setItem('hostToken', data.hostToken);

        const newUrl = new URL(window.location.href);
        newUrl.searchParams.set('sessionKey', data.sessionKey);
        newUrl.searchParams.set('hostToken', data.hostToken);
        window.history.replaceState({}, '', newUrl);

        await initializeSession();
    } catch (error) {
        console.error('Failed to create session:', error);
        setStatus('Failed to create session: ' + error.message, true);
    }
}

// Join session
async function initializeSession() {
    try {
        console.log('Initializing session with key:', sessionKey);

        const urlHostToken = urlParams.get('hostToken');
        const storedHostToken = sessionStorage.getItem('hostToken');
        const hostToken = urlHostToken || storedHostToken;

        const validateUrl = `${BACKEND_URL}/api/sessions/${encodeURIComponent(sessionKey)}/validate`;
        const urlWithParams = hostToken ? `${validateUrl}?hostToken=${encodeURIComponent(hostToken)}` : validateUrl;

        console.log('Sending validation request to:', urlWithParams);
        const response = await fetch(urlWithParams);

        if (!response.ok) {
            throw new Error(`HTTP error! status: ${response.status}`);
        }

        const data = await response.json();
        console.log('Session validation response:', data);

        if (!data.valid) {
            setStatus('Invalid session key', true);
            return;
        }

        isHost = data.isHost;
        console.log('User role:', isHost ? 'Host' : 'Participant');

        if (isHost && hostToken && !urlHostToken) {
            const newUrl = new URL(window.location.href);
            newUrl.searchParams.set('hostToken', hostToken);
            window.history.replaceState({}, '', newUrl);
        }

        // After receiving streaming URL
        const streamingUrl = data.streaming_url;
        console.log(streamingUrl)

        connectWebSocket(streamingUrl);
        
        // Clear existing sources and create new dynamic source
        const videoElement = document.getElementById('videoPlayer');
        videoElement.innerHTML = ''; // Clear any existing sources
        const source = document.createElement('source');
        source.src = `${streamingUrl}/api/video`;
        source.type = 'video/mp4';
        videoElement.appendChild(source);
        
        // Load the new source
        videoElement.load();
        videoElement.play().catch(error => {
            // Show play button if autoplay is blocked
            if (error.name === 'NotAllowedError') {
                const playBtn = document.getElementById('playPauseBtn');
                playBtn.disabled = false;
                playBtn.textContent = 'Start Playback';
                setStatus('Click play to start video', false);
            }
        });        

        document.getElementById('sessionKeyDisplay').textContent = sessionKey;
        document.getElementById('userRole').textContent = isHost ? 'Host' : 'Participant';
        updateControls();
        attachCustomControlListeners();

        videoElement.addEventListener('error', (e) => {
            console.error('Video error:', videoElement.error);
            setStatus(`Video error: ${videoElement.error.message}`, true);
        });

        videoElement.addEventListener('stalled', () => {
            setStatus('Video stream stalled - reconnecting...', true);
            videoElement.load(); // Attempt to reload
        });
        
        videoElement.addEventListener('loadeddata', () => {
            setStatus('Video stream connected', false);
            if (isHost) {
                videoElement.play().catch(e => {
                    console.log('Autoplay blocked - waiting for user interaction');
                });
            }
        });

    } catch (error) {
        console.error('Session initialization error:', error);
        setStatus('Failed to initialize session: ' + error.message, true);
    }
}

// Entry
if (sessionKey) {
    console.log('Session key found, starting initialization');
    initializeSession();
} else {
    console.log('No session key found, creating new session');
    createNewSession();
}


function reloadVideoSource() {
    const currentTime = videoElement.currentTime;
    videoElement.src = `${streamingUrl}/api/video?t=${Date.now()}`;
    videoElement.currentTime = currentTime;
    videoElement.play();
}

// Handle DOM video events
videoElement.addEventListener('play', () => sendPlayerState('play'));
videoElement.addEventListener('pause', () => sendPlayerState('pause'));
videoElement.addEventListener('seeked', () => sendPlayerState('seek'));

videoElement.addEventListener('timeupdate', () => {
    document.getElementById('currentTime').textContent = formatTime(videoElement.currentTime);
    document.getElementById('seekBar').value = videoElement.currentTime;
});

videoElement.addEventListener('loadedmetadata', () => {
    document.getElementById('seekBar').max = videoElement.duration;
    document.getElementById('duration').textContent = formatTime(videoElement.duration);
});

// Custom button listeners
function attachCustomControlListeners() {
    const playPauseBtn = document.getElementById('playPauseBtn');
    const seekBar = document.getElementById('seekBar');

    playPauseBtn.addEventListener('click', () => {
        if (!isHost) return;
        if (videoElement.paused) {
            videoElement.play();
        } else {
            videoElement.pause();
        }
    });

    seekBar.addEventListener('input', (e) => {
        if (!isHost) return;
        videoElement.currentTime = parseFloat(e.target.value);
    });
}

// Helper: format time
function formatTime(seconds) {
    const date = new Date(0);
    date.setSeconds(seconds);
    return date.toISOString().substr(11, 8);
}

// Status message display
function setStatus(message, isError) {
    const statusElement = document.getElementById('statusMessage');
    statusElement.textContent = message;
    statusElement.style.color = isError ? '#D32F2F' : '#388E3C';
    statusElement.style.display = 'block';
    setTimeout(() => {
        statusElement.style.display = 'none';
    }, 3000);
}

// Send player state to backend
function sendPlayerState(type) {
    // console.log(ws)
    if (!isHost || !ws || ws.readyState !== WebSocket.OPEN) return;

    const video_state = {
        type: 'stateUpdate',
        state: {
            // sessionKey: sessionStorage.getItem('sessionKey'),
            // hostToken: sessionStorage.getItem('hostToken'),
            paused: videoElement.paused,
            currentTime: videoElement.currentTime,
            playbackRate: videoElement.playbackRate,
            timestamp: Date.now()
        }
    };
    ws.send(JSON.stringify(video_state));
    // console.log("Sent update")
}

// When playing the video for the first time through the host, send a play sync as host can join whenever.