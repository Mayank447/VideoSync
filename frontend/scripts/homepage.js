const MAIN_SERVER_URL = 'http://localhost:8080';

async function createSession() {
    try {
        const response = await fetch(`${MAIN_SERVER_URL}/api/sessions`, {
            method: 'POST'
        });
        
        if (!response.ok) throw new Error('Failed to create session');
        
        const { sessionKey, streaming_url, hostToken } = await response.json();
        
        // Store host token in sessionStorage
        sessionStorage.setItem("sessionKey", sessionKey);
        sessionStorage.setItem("hostToken", hostToken);
        
        // Update display
        const sessionKeyElement = document.getElementById('sessionKey');
        sessionKeyElement.textContent = sessionKey;
        document.getElementById('sessionKeyContainer').style.display = 'block';

        // Copy to clipboard
        try {
            if (navigator.clipboard) {
                await navigator.clipboard.writeText(sessionKey);
                showTemporaryMessage('Session key copied to clipboard!', 'success');
            } else {
                const textArea = document.createElement('textarea');
                textArea.value = sessionKey;
                document.body.appendChild(textArea);
                textArea.select();
                document.execCommand('copy');
                document.body.removeChild(textArea);
                showTemporaryMessage('Session key copied!', 'success');
            }
        } catch (error) {
            console.warn('Clipboard error:', error);
        }

        console.log("Stored sessionKey:", sessionStorage.getItem("sessionKey"));
        console.log("Stored hostToken:", sessionStorage.getItem("hostToken"));
        window.location.href = `pages/player.html?sessionKey=${encodeURIComponent(sessionKey)}&host=true`;

    } catch (error) {
        showTemporaryMessage('Error creating session: ' + error.message, 'error');
        console.error('Create session error:', error);
    }
}

async function joinSession() {
    const sessionKey = document.getElementById('joinKey').value.trim();
    if (!sessionKey) {
        showError('Please enter a session key');
        return;
    }

    try {
        const response = await fetch(
            `${MAIN_SERVER_URL}/api/sessions/${encodeURIComponent(sessionKey)}/validate`
        );
        
        if (!response.ok) throw new Error('Invalid session key');
        
        const { valid } = await response.json();
        if (!valid) throw new Error('Invalid session key');
        
        window.location.href = `pages/player.html?sessionKey=${encodeURIComponent(sessionKey)}`;
        
    } catch (error) {
        showError(error.message);
        console.error('Join error:', error);
    }
}

function showTemporaryMessage(message, type = 'info') {
    const statusDiv = document.getElementById('statusMessages');
    statusDiv.textContent = message;
    statusDiv.className = `status-${type}`;
    statusDiv.style.display = 'block';
    setTimeout(() => statusDiv.style.display = 'none', 3000);
}

function showError(message) {
    const errorElement = document.getElementById('errorMessage');
    errorElement.textContent = message;
    errorElement.style.display = 'block';
    setTimeout(() => errorElement.style.display = 'none', 3000);
}