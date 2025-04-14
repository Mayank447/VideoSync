async function createSession() {
    try {
        const response = await fetch('http://localhost:8080/api/sessions', {
            method: 'POST'
        });
        
        if (!response.ok) throw new Error('Failed to create session');
        
        const { sessionKey } = await response.json();
        
        // Display session key
        document.getElementById('sessionKey').textContent = sessionKey;
        document.getElementById('sessionKeyContainer').style.display = 'block';
        
        // Automatically copy to clipboard
        navigator.clipboard.writeText(sessionKey).then(() => {
            alert('Session key copied to clipboard!');
        });
        
    } catch (error) {
        console.error('Error creating session:', error);
        alert('Error creating session. Please try again.');
    }
}

async function joinSession() {
    const sessionKey = document.getElementById('joinKey').value.trim();
    const errorElement = document.getElementById('errorMessage');
    
    if (!sessionKey) {
        errorElement.textContent = 'Please enter a session key';
        errorElement.style.display = 'block';
        return;
    }

    try {
        // Validate session key
        const response = await fetch(`http://localhost:8080/api/sessions/${sessionKey}/validate`);
        
        if (!response.ok) throw new Error('Invalid session key');
        
        // Redirect to player page with session key
        window.location.href = `/pages/video_player.html?sessionKey=${encodeURIComponent(sessionKey)}`;
        
    } catch (error) {
        errorElement.textContent = 'Invalid session key. Please check and try again.';
        errorElement.style.display = 'block';
        console.error('Join error:', error);
    }
}