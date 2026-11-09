"""Great-circle distances between points on the Earth."""

import math

EARTH_RADIUS_KM = 6371.0


def haversine(lat1, lon1, lat2, lon2):
    """Return the great-circle distance in kilometres between two points
    given as latitude and longitude in degrees."""
    phi1, phi2 = math.radians(lat1), math.radians(lat2)
    dphi = math.radians(lat2 - lat1)
    dlambda = math.radians(lon2 - lon1)
    a = math.sin(dphi / 2) ** 2 + math.cos(phi1) * math.cos(phi2) * math.sin(dlambda / 2) ** 2
    return 2 * EARTH_RADIUS_KM * math.asin(math.sqrt(a))


def nearest(origin, places):
    """The place closest to origin, by haversine distance."""
    return min(places, key=lambda p: haversine(origin[0], origin[1], p[0], p[1]))
