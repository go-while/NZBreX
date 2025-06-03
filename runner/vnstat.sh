#!/bin/bash
# This script generates vnStat statistics PNG images and creates a static HTML page
# in the /var/www/html directory to display them.

# Exit immediately if a command exits with a non-zero status.
set -e

# Define the web directory path
WWW_PATH="/var/www/html"
HTML_FILE="${WWW_PATH}/vnstat.html"

# Check if vnStat is installed
if ! command -v vnstati &> /dev/null; then
  echo "Error: vnstati is not installed. Please install vnStat before running this script."
  exit 1
fi

echo "Generating vnStat PNG images..."

echo "Generating summary image..."
vnstati -s -o "${WWW_PATH}/vnstat_summary.png"

echo "Generating top10 image..."
vnstati -t -o "${WWW_PATH}/vnstat_top10.png"

echo "Generating hours image..."
vnstati -h -o "${WWW_PATH}/vnstat_hours.png"

echo "Generating daily image..."
vnstati -d -o "${WWW_PATH}/vnstat_daily.png"

#echo "Generating weekly image..."
#vnstati -w -o "${WWW_PATH}/vnstat_weekly.png"

echo "Generating monthly image..."
vnstati -m -o "${WWW_PATH}/vnstat_monthly.png"

#echo "Generating yearly image..."
#vnstati -y -o "${WWW_PATH}/vnstat_yearly.png"

echo "All images generated and saved to ${WWW_PATH}"

# Create the static HTML file

echo "Creating static HTML file at ${HTML_FILE}..."

cat <<EOF > "$HTML_FILE"
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>vnStat Traffic Statistics</title>
  <style>
    body {
      font-family: Arial, sans-serif;
      background-color: #f4f4f4;
      margin: 0;
      padding: 20px;
    }
    h1 {
      text-align: center;
      color: #333;
      margin-bottom: 20px;
    }
    .container {
      display: flex;
      flex-wrap: wrap;
      justify-content: center;
    }
    .stat {
      background: #fff;
      margin: 10px;
      padding: 15px;
      border-radius: 4px;
      box-shadow: 0 2px 5px rgba(0,0,0,0.1);
      flex: 0 0 90%;
      max-width: 500px;
    }
    .stat img {
      max-width: 100%;
      display: block;
      margin: 0 auto;
    }
    .stat h2 {
      text-align: center;
      color: #555;
    }
    @media (min-width: 600px) {
      .stat {
        flex: 0 0 45%;
      }
    }
    @media (min-width: 900px) {
      .stat {
        flex: 0 0 30%;
      }
    }
  </style>
</head>
<body>
  <h1>vnStat Traffic Statistics</h1>
  <div class="container">
    <div class="stat">
      <h2>Summary</h2>
      <img src="vnstat_summary.png" alt="vnStat Summary">
    </div>
    <div class="stat">
      <h2>Top10</h2>
      <img src="vnstat_top10.png" alt="vnStat Top10">
    </div>
    <div class="stat">
      <h2>Daily</h2>
      <img src="vnstat_hours.png" alt="vnStat Hours">
    </div>
    <div class="stat">
      <h2>Daily</h2>
      <img src="vnstat_daily.png" alt="vnStat Daily">
    </div>
    <!--
    <div class="stat">
      <h2>Weekly</h2>
      <img src="vnstat_weekly.png" alt="vnStat Weekly">
    </div>
    -->
    <div class="stat">
      <h2>Monthly</h2>
      <img src="vnstat_monthly.png" alt="vnStat Monthly">
    </div>
    <!--
    <div class="stat">
      <h2>Yearly</h2>
      <img src="vnstat_yearly.png" alt="vnStat Yearly">
    </div>
    -->
  </div>
</body>
</html>
EOF

echo "Static HTML page successfully created!"
